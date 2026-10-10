// Package poller is the monitoring engine: per-device ping + SNMP polls on a
// ticker, bandwidth rate calculation, and up/down state events.
package poller

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"netgold/internal/store"
)

// TransitionNotifier receives device up/down transitions at the exact
// atomic transition point (see PollDevice). Implemented by the email
// alert package; nil disables notifications.
type TransitionNotifier interface {
	DeviceTransition(d *store.Device, newStatus string, downtime time.Duration)
}

type Engine struct {
	Store    *store.Store
	Interval time.Duration
	Notifier TransitionNotifier

	mu       sync.Mutex
	trackIn  map[string]*RateCalc
	trackOut map[string]*RateCalc
	demoT    float64

	monMu      sync.Mutex
	monLastRun map[int64]time.Time

	// pollLocks holds one mutex per device ID so a poll for a given device
	// never overlaps another poll for the same device, whichever entry
	// point started it (ticker, add-device, poll-now). Entries are tiny
	// and never removed; device counts are small.
	pollLocks sync.Map // int64 -> *sync.Mutex
}

// deviceLock returns the mutex serialising polls for one device.
func (e *Engine) deviceLock(id int64) *sync.Mutex {
	m, _ := e.pollLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func New(st *store.Store, interval time.Duration) *Engine {
	return &Engine{Store: st, Interval: interval,
		trackIn: map[string]*RateCalc{}, trackOut: map[string]*RateCalc{},
		monLastRun: map[int64]time.Time{}}
}

func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.Interval)
	defer t.Stop()
	e.PollAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.PollAll(ctx)
		}
	}
}

// Maintenance: hourly downsampling (raw 7 days -> 5-min avg, keep 90 days).
func (e *Engine) Maintenance(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = e.Store.Downsample(time.Now(), 7*24*time.Hour, 90*24*time.Hour)
			// v2.1 data (flow buckets, syslog) follows the same 90-day
			// retention policy as samples.
			_ = e.Store.PruneFlowsAndLogs(time.Now(), 90*24*time.Hour)
		}
	}
}

func (e *Engine) PollAll(ctx context.Context) {
	devs, err := e.Store.ListDevices()
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range devs {
		d := devs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			e.PollDevice(ctx, &d)
		}()
	}
	wg.Wait()
}

func (e *Engine) PollDevice(ctx context.Context, d *store.Device) {
	_ = ctx
	if d == nil {
		return
	}
	// Monitoring switched off for this device (v3.6): skip it
	// entirely — no ping, no SNMP, no service monitors, and no
	// status writes, so its stored status stays exactly as it was
	// when monitoring was turned off.
	if d.MonitoringDisabled {
		return
	}
	// Serialise per device: if a poll for this device is already running,
	// wait for it rather than running concurrently — two overlapping polls
	// could otherwise observe the same status transition twice. (Waiting,
	// not skipping, keeps poll-now and add-device polls meaningful; the
	// store's atomic transition is the second line of defence.)
	lock := e.deviceLock(d.ID)
	lock.Lock()
	defer lock.Unlock()
	now := time.Now()
	if d.Type == "demo" {
		e.pollDemo(d, now)
		return
	}
	pr := Ping(d.IP, PingAttemptTimeout)
	up := pr.AvgMs >= 0

	var sd *SnmpData
	if !d.SnmpDisabled {
		if data, err := FetchAllCreds(d.IP, CredsOf(d)); err == nil {
			sd = data
			up = true // answers SNMP even if ICMP is blocked
			if data.SysName != "" {
				d.SysName = data.SysName
			}
			if data.SysDescr != "" {
				d.SysDescr = data.SysDescr
			}
			// Auto-classify generic devices from the sysObjectID
			// enterprise prefix (the legacy system DeviceType table style).
			if (d.Type == "" || d.Type == "unknown") && data.SysObjectID != "" {
				if t := ClassifySysObjectID(data.SysObjectID); t != "" {
					d.Type = t
					_ = e.Store.SetDeviceType(d.ID, t)
				}
			}
		}
	}

	if up {
		if pr.AvgMs >= 0 {
			e.Store.AddSample(d.ID, nil, "ping_ms", now, pr.AvgMs)
			e.Store.AddSample(d.ID, nil, "ping_loss", now, pr.Loss)
		}
		if sd != nil {
			e.storeSnmp(d, sd, now)
		}
	}

	status := "down"
	if up {
		status = "up"
	}
	// Atomic transition: the store compares against the stored status and
	// inserts the event itself, in the same transaction, only on a real
	// change — a stale in-memory snapshot can no longer double-fire.
	msg := fmt.Sprintf("%s (%s) is now %s", d.Name, d.IP, status)
	transitioned, _ := e.Store.TransitionDeviceStatus(d, status, status, msg)
	if transitioned && e.Notifier != nil {
		var downtime time.Duration
		if status == "up" {
			if ev, ok := e.Store.LastDeviceEvent(d.ID, "down"); ok {
				downtime = time.Since(time.Unix(ev.TS, 0))
			}
		}
		dev := *d
		go e.Notifier.DeviceTransition(&dev, status, downtime)
	}

	// Service monitors run after the device poll, each on its own
	// per-monitor interval. Their results never change device status.
	e.runServiceMonitors(d, now)
}

// runServiceMonitors executes the device's due service checks and records
// samples + monitor-level state transitions.
func (e *Engine) runServiceMonitors(d *store.Device, now time.Time) {
	mons, err := e.Store.ListDeviceMonitors(d.ID)
	if err != nil {
		return
	}
	for _, m := range mons {
		if m.Kind != "service" {
			continue
		}
		interval := e.Interval
		if m.IntervalSec > 0 {
			interval = time.Duration(m.IntervalSec) * time.Second
		}
		e.monMu.Lock()
		last, ran := e.monLastRun[m.ID]
		if ran && now.Sub(last) < interval {
			e.monMu.Unlock()
			continue
		}
		e.monLastRun[m.ID] = now
		e.monMu.Unlock()

		res := RunServiceCheck(m, d.IP)
		mid := int(m.ID)
		upVal := 0.0
		state := "down"
		if res.Up {
			upVal, state = 1, "up"
		}
		e.Store.AddSample(d.ID, &mid, "monitor_up", now, upVal)
		e.Store.AddSample(d.ID, &mid, "monitor_ms", now, float64(res.Latency.Microseconds())/1000.0)
		label := m.Name
		if m.Port > 0 {
			label = fmt.Sprintf("%s (port %d)", m.Name, m.Port)
		}
		detail := fmt.Sprintf("Monitor %s on %s (%s) is %s: %s", label, d.Name, d.IP, state, res.Detail)
		_, _ = e.Store.TransitionMonitorState(m, state, detail)
	}
}

func (e *Engine) storeSnmp(d *store.Device, sd *SnmpData, now time.Time) {
	e.Store.AddSample(d.ID, nil, "uptime_s", now, float64(sd.UpTime)/100)
	if sd.CPU != nil {
		e.Store.AddSample(d.ID, nil, "cpu_pct", now, *sd.CPU)
	}
	if sd.Mem != nil {
		e.Store.AddSample(d.ID, nil, "mem_pct", now, *sd.Mem)
	}
	for _, ifd := range sd.Ifaces {
		op := "down"
		if ifd.OperUp {
			op = "up"
		}
		e.Store.UpsertInterface(store.Interface{DeviceID: d.ID, IfIndex: ifd.Index, Name: ifd.Name, SpeedBps: ifd.SpeedBps, OperStatus: op, MAC: ifd.MAC})
		if !ifd.HasCount {
			continue
		}
		key := fmt.Sprintf("%d:%d", d.ID, ifd.Index)
		e.mu.Lock()
		tin := e.trackIn[key]
		if tin == nil {
			tin = NewRateCalc(ifd.Bits)
			e.trackIn[key] = tin
		}
		tout := e.trackOut[key]
		if tout == nil {
			tout = NewRateCalc(ifd.Bits)
			e.trackOut[key] = tout
		}
		e.mu.Unlock()
		idx := ifd.Index
		if bps, ok := tin.Add(ifd.InOctets, sd.UpTime, now); ok {
			e.Store.AddSample(d.ID, &idx, "if_in_bps", now, bps)
		}
		if bps, ok := tout.Add(ifd.OutOctets, sd.UpTime, now); ok {
			e.Store.AddSample(d.ID, &idx, "if_out_bps", now, bps)
		}
	}
}

// --- Demo device: synthetic samples so graphs render with no real gear ---
func (e *Engine) pollDemo(d *store.Device, now time.Time) {
	e.mu.Lock()
	e.demoT += 1
	t := e.demoT
	e.mu.Unlock()
	wave := func(base, amp, period float64) float64 {
		return base + amp*(0.5+0.5*math.Sin(t*2*math.Pi/period)) + rand.Float64()*amp*0.15
	}
	e.Store.AddSample(d.ID, nil, "ping_ms", now, wave(4, 6, 20))
	e.Store.AddSample(d.ID, nil, "ping_loss", now, 0)
	e.Store.AddSample(d.ID, nil, "cpu_pct", now, wave(25, 40, 30))
	e.Store.AddSample(d.ID, nil, "mem_pct", now, wave(50, 20, 45))
	e.Store.AddSample(d.ID, nil, "uptime_s", now, 86400*14+t*60)
	ifaces := []struct {
		idx  int
		name string
		base float64
	}{
		{1, "WAN (ether1)", 55e6},
		{2, "LAN (ether2)", 22e6},
	}
	for _, f := range ifaces {
		e.Store.UpsertInterface(store.Interface{DeviceID: d.ID, IfIndex: f.idx, Name: f.name, SpeedBps: 1_000_000_000, OperStatus: "up"})
		idx := f.idx
		e.Store.AddSample(d.ID, &idx, "if_in_bps", now, wave(f.base, f.base*0.9, 15+float64(f.idx)*7))
		e.Store.AddSample(d.ID, &idx, "if_out_bps", now, wave(f.base*0.45, f.base*0.4, 18+float64(f.idx)*5))
	}
	msg := fmt.Sprintf("%s (%s) is now up", d.Name, d.IP)
	e.Store.TransitionDeviceStatus(d, "up", "up", msg)
}
