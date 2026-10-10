// Package report computes per-device downtime (outages, total downtime,
// uptime %) for a period from NetGold's up/down event history, and renders
// the result as JSON data, an Excel workbook or a PDF document.
//
// Semantics: in NetGold a monitored "link" or "site" is a device entry
// (exactly as in the legacy system data NetGold imports — each district link
// is a device with monitors). Downtime is computed from that device's
// up/down events. Map/diagram lines are pure geometry with no monitoring
// of their own, so they never appear as separate report rows.
//
// A device's effective period starts at max(report start, the time the
// device was created in NetGold); devices whose creation time is unknown
// (imported before creation tracking) use the full report period.
package report

import (
	"fmt"
	"sort"
	"time"

	"netgold/internal/store"
)

// DeviceInfo is the report's view of one device.
type DeviceInfo struct {
	ID        int64
	Name      string
	Group     string
	IP        string
	CreatedAt int64 // unix seconds; 0 = unknown (full period applies)
	Status    string
}

type Outage struct {
	DeviceID    int64  `json:"device_id"`
	DeviceName  string `json:"device_name"`
	Group       string `json:"group"`
	IP          string `json:"ip"`
	DownTS      int64  `json:"down_ts"`         // effective start (clipped to the period when Clipped)
	UpTS        *int64 `json:"up_ts,omitempty"` // nil while the outage is ongoing
	DurationSec int64  `json:"duration_sec"`    // seconds counted inside the period
	Ongoing     bool   `json:"ongoing"`         // device still down at period end
	Clipped     bool   `json:"clipped"`         // outage began before the period start
}

type DeviceSummary struct {
	DeviceID         int64   `json:"device_id"`
	DeviceName       string  `json:"device_name"`
	Group            string  `json:"group"`
	IP               string  `json:"ip"`
	Outages          int     `json:"outages"`
	TotalDowntimeSec int64   `json:"total_downtime_sec"`
	LongestSec       int64   `json:"longest_outage_sec"`
	UptimePct        float64 `json:"uptime_pct"`
	CurrentlyDown    bool    `json:"currently_down"`
}

type Totals struct {
	Devices          int     `json:"devices"`
	Outages          int     `json:"outages"`
	TotalDowntimeSec int64   `json:"total_downtime_sec"`
	LongestSec       int64   `json:"longest_outage_sec"`
	UptimePct        float64 `json:"uptime_pct"`
}

type Report struct {
	From        int64           `json:"from"`
	To          int64           `json:"to"`
	GeneratedAt int64           `json:"generated_at"`
	Devices     []DeviceSummary `json:"devices"`
	Outages     []Outage        `json:"outages"`
	Totals      Totals          `json:"totals"`
	Note        string          `json:"note"`
}

const SemanticsNote = "Downtime is computed from each monitored device's up/down events. " +
	"In NetGold (as in the imported legacy data) a monitored link or site is a device entry; " +
	"diagram lines are drawings without their own monitoring and are not separate report rows. " +
	"An outage marked \"clipped\" began before the report period — only the part inside the period is counted."

// Compute builds the downtime report for [from, to] from the devices and
// their up/down events. events must include, per device, the latest event
// before `from` (store.ReportEvents does this) so a device that starts the
// period already down is handled correctly.
func Compute(devices []DeviceInfo, events []store.Event, from, to time.Time) Report {
	fromTS, toTS := from.Unix(), to.Unix()
	byDevice := map[int64][]store.Event{}
	for _, e := range events {
		if e.DeviceID == nil || (e.Kind != "up" && e.Kind != "down") {
			continue
		}
		byDevice[*e.DeviceID] = append(byDevice[*e.DeviceID], e)
	}

	rep := Report{
		From:        fromTS,
		To:          toTS,
		GeneratedAt: time.Now().Unix(),
		Devices:     []DeviceSummary{},
		Outages:     []Outage{},
		Note:        SemanticsNote,
	}
	var totalPeriod int64

	for _, d := range devices {
		effStart := fromTS
		if d.CreatedAt > effStart {
			effStart = d.CreatedAt
		}
		if effStart >= toTS {
			continue // device did not exist during the period
		}
		periodLen := toTS - effStart
		totalPeriod += periodLen

		evs := byDevice[d.ID]
		sort.SliceStable(evs, func(i, j int) bool {
			if evs[i].TS != evs[j].TS {
				return evs[i].TS < evs[j].TS
			}
			return evs[i].ID < evs[j].ID
		})

		// Replay state. stateDown tracks whether the device is down;
		// openTS is when the current (possibly pre-period) outage began.
		stateDown := false
		var openTS int64
		var devOutages []Outage
		closeOutage := func(upTS int64, ongoing bool) {
			start := openTS
			clipped := false
			if start < effStart {
				start = effStart
				clipped = true
			}
			if upTS < start { // defensive: never negative
				upTS = start
			}
			o := Outage{
				DeviceID: d.ID, DeviceName: d.Name, Group: d.Group, IP: d.IP,
				DownTS: start, DurationSec: upTS - start,
				Ongoing: ongoing, Clipped: clipped,
			}
			if !ongoing {
				u := upTS
				o.UpTS = &u
			}
			devOutages = append(devOutages, o)
		}
		for _, e := range evs {
			switch e.Kind {
			case "down":
				if !stateDown {
					stateDown = true
					openTS = e.TS
				}
			case "up":
				if stateDown {
					if e.TS >= effStart {
						closeOutage(e.TS, false)
					}
					stateDown = false
				}
			}
		}
		if stateDown {
			closeOutage(toTS, true)
		}

		sum := DeviceSummary{DeviceID: d.ID, DeviceName: d.Name, Group: d.Group, IP: d.IP, CurrentlyDown: stateDown}
		for _, o := range devOutages {
			sum.Outages++
			sum.TotalDowntimeSec += o.DurationSec
			if o.DurationSec > sum.LongestSec {
				sum.LongestSec = o.DurationSec
			}
		}
		if periodLen > 0 {
			sum.UptimePct = 100 * (1 - float64(sum.TotalDowntimeSec)/float64(periodLen))
		} else {
			sum.UptimePct = 100
		}
		rep.Devices = append(rep.Devices, sum)
		rep.Outages = append(rep.Outages, devOutages...)
		rep.Totals.Outages += sum.Outages
		rep.Totals.TotalDowntimeSec += sum.TotalDowntimeSec
		if sum.LongestSec > rep.Totals.LongestSec {
			rep.Totals.LongestSec = sum.LongestSec
		}
	}
	rep.Totals.Devices = len(rep.Devices)
	if totalPeriod > 0 {
		rep.Totals.UptimePct = 100 * (1 - float64(rep.Totals.TotalDowntimeSec)/float64(totalPeriod))
	} else {
		rep.Totals.UptimePct = 100
	}

	// Summary: worst downtime first. Details: chronological.
	sort.SliceStable(rep.Devices, func(i, j int) bool {
		if rep.Devices[i].TotalDowntimeSec != rep.Devices[j].TotalDowntimeSec {
			return rep.Devices[i].TotalDowntimeSec > rep.Devices[j].TotalDowntimeSec
		}
		return rep.Devices[i].DeviceName < rep.Devices[j].DeviceName
	})
	sort.SliceStable(rep.Outages, func(i, j int) bool {
		if rep.Outages[i].DownTS != rep.Outages[j].DownTS {
			return rep.Outages[i].DownTS < rep.Outages[j].DownTS
		}
		return rep.Outages[i].DeviceName < rep.Outages[j].DeviceName
	})
	return rep
}

// FromStore assembles a Report straight from the store for [from, to].
func FromStore(s *store.Store, from, to time.Time) (Report, error) {
	devs, err := s.ListDevices()
	if err != nil {
		return Report{}, err
	}
	groups, err := s.ListGroups()
	if err != nil {
		return Report{}, err
	}
	gname := map[int64]string{}
	for _, g := range groups {
		gname[g.ID] = g.Name
	}
	infos := make([]DeviceInfo, 0, len(devs))
	for _, d := range devs {
		info := DeviceInfo{ID: d.ID, Name: d.Name, IP: d.IP, CreatedAt: d.CreatedAt, Status: d.Status}
		if d.GroupID != nil {
			info.Group = gname[*d.GroupID]
		}
		infos = append(infos, info)
	}
	evs, err := s.ReportEvents(from, to)
	if err != nil {
		return Report{}, err
	}
	return Compute(infos, evs, from, to), nil
}

// FormatDuration renders seconds the way the reports show them:
// "45s", "12m 30s", "2h 14m", "3d 4h".
func FormatDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// FormatTS renders a unix timestamp for report documents (server local time).
func FormatTS(ts int64) string {
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}
