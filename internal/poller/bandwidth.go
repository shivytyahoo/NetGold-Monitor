package poller

import "time"

// RateCalc turns monotonically increasing octet counters into bits/sec.
// It guards against the three classic MRTG/Cacti failure modes:
//   - counter wrap (32-bit counters wrap at 2^32, 64-bit at 2^64)
//   - device reboot (sysUpTime goes backwards -> discard baseline)
//   - first sample (no baseline yet -> no rate)
type RateCalc struct {
	lastCounter uint64
	lastUptime  uint32 // seconds*100 from SNMP sysUpTime (TimeTicks)
	lastTime    time.Time
	hasBase     bool
	bits        uint // counter width: 32 or 64
}

func NewRateCalc(bits uint) *RateCalc { return &RateCalc{bits: bits} }

// Add feeds one counter reading. Returns (bps, true) when a valid rate
// could be computed, (_, false) when this reading only sets a baseline.
func (r *RateCalc) Add(counter uint64, uptimeTicks uint32, now time.Time) (float64, bool) {
	if !r.hasBase {
		r.set(counter, uptimeTicks, now)
		return 0, false
	}
	// Reboot: sysUpTime decreased -> counters reset, drop the baseline.
	if uptimeTicks < r.lastUptime {
		r.set(counter, uptimeTicks, now)
		return 0, false
	}
	dt := now.Sub(r.lastTime).Seconds()
	if dt <= 0 {
		return 0, false
	}
	var delta uint64
	if counter >= r.lastCounter {
		delta = counter - r.lastCounter
	} else {
		// Wrap: distance from last counter to the wrap point + new counter.
		if r.bits == 32 {
			delta = (uint64(1)<<32 - r.lastCounter) + counter
		} else {
			delta = (uint64(0) - r.lastCounter) + counter // 2^64 wrap via unsigned overflow
		}
	}
	r.set(counter, uptimeTicks, now)
	// Sanity: a wrapped delta implying an absurd rate (>10 Tbps) means the
	// counter was reset without an uptime change (e.g. interface recreated).
	bps := float64(delta) * 8 / dt
	if bps > 1e13 {
		return 0, false
	}
	return bps, true
}

func (r *RateCalc) set(c uint64, u uint32, t time.Time) {
	r.lastCounter, r.lastUptime, r.lastTime, r.hasBase = c, u, t, true
}
