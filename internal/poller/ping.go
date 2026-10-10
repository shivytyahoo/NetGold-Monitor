package poller

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// Ping defaults aligned with the legacy system's Ping monitor (installer
// study, bcp_ActiveMonitorTypeData): timeout 1 s per attempt, 1 retry
// (2 attempts total), 32-byte payload. the legacy system's discovery role
// templates use the same numbers with a 2 s timeout.
const (
	PingAttemptTimeout = 1 * time.Second
	PingRetries        = 1
	PingPayloadBytes   = 32
)

// PingResult: AvgMs < 0 means unreachable.
type PingResult struct {
	AvgMs float64
	Loss  float64 // percent
}

// Ping tries the pro-bing library first (privileged ICMP, works as admin/root
// and on Windows with admin rights), then falls back to the OS ping binary,
// which needs no privileges. The timeout argument is the per-attempt
// timeout; PingRetries retries follow the first attempt.
func Ping(host string, timeout time.Duration) PingResult {
	if timeout <= 0 {
		timeout = PingAttemptTimeout
	}
	if r, err := pingLib(host, timeout); err == nil {
		return r
	}
	if r, err := pingExec(host, timeout); err == nil {
		return r
	}
	return PingResult{AvgMs: -1, Loss: 100}
}

func pingLib(host string, timeout time.Duration) (PingResult, error) {
	attempts := PingRetries + 1
	mk := func(privileged bool) (*probing.Pinger, error) {
		p, err := probing.NewPinger(host)
		if err != nil {
			return nil, err
		}
		p.Count = attempts
		p.Size = PingPayloadBytes
		// Overall deadline: one timeout per attempt, plus slack for the
		// inter-packet interval so the last attempt can time out fully.
		p.Timeout = time.Duration(attempts)*timeout + 500*time.Millisecond
		p.Interval = 100 * time.Millisecond
		p.SetPrivileged(privileged)
		return p, nil
	}
	p, err := mk(true)
	if err != nil {
		return PingResult{}, err
	}
	if err := p.Run(); err != nil {
		// retry unprivileged (ICMP echo sockets, Linux ping_group_range / Windows)
		p2, err2 := mk(false)
		if err2 != nil {
			return PingResult{}, err
		}
		if err := p2.Run(); err != nil {
			return PingResult{}, err
		}
		p = p2
	}
	st := p.Statistics()
	if st.PacketsRecv == 0 {
		return PingResult{AvgMs: -1, Loss: 100}, nil
	}
	return PingResult{AvgMs: float64(st.AvgRtt.Microseconds()) / 1000.0, Loss: st.PacketLoss}, nil
}

var (
	reTime = regexp.MustCompile(`time[=<]\s*([\d.]+)\s*ms`)
	reLoss = regexp.MustCompile(`(\d+(?:\.\d+)?)%\s*(?:packet\s*)?loss`)
)

func pingExec(host string, timeout time.Duration) (PingResult, error) {
	attempts := PingRetries + 1
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", strconv.Itoa(attempts), "-w", strconv.Itoa(int(timeout.Milliseconds())), "-l", strconv.Itoa(PingPayloadBytes), host)
	} else {
		secs := int(timeout.Seconds())
		if secs < 1 {
			secs = 1
		}
		cmd = exec.Command("ping", "-c", strconv.Itoa(attempts), "-W", strconv.Itoa(secs), "-s", strconv.Itoa(PingPayloadBytes), host)
	}
	out, _ := cmd.Output() // exit code is non-zero on total loss; output still parses
	s := string(out)
	times := reTime.FindAllStringSubmatch(s, -1)
	if len(times) == 0 {
		return PingResult{AvgMs: -1, Loss: 100}, nil
	}
	sum := 0.0
	for _, m := range times {
		v, _ := strconv.ParseFloat(m[1], 64)
		sum += v
	}
	loss := 100 * (1 - float64(len(times))/float64(attempts))
	if m := reLoss.FindStringSubmatch(s); m != nil {
		loss, _ = strconv.ParseFloat(m[1], 64)
	}
	return PingResult{AvgMs: sum / float64(len(times)), Loss: loss}, nil
}
