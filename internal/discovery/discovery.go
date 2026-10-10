// Package discovery scans a CIDR subnet: ping sweep + SNMP probe.
package discovery

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"netgold/internal/poller"
)

type Found struct {
	IP        string  `json:"ip"`
	LatencyMs float64 `json:"latency_ms"`
	SysName   string  `json:"sys_name,omitempty"`
	SysDescr  string  `json:"sys_descr,omitempty"`
	SNMP      bool    `json:"snmp"`
}

// Scan probes every host in the CIDR (capped at 1024 hosts) concurrently.
func Scan(cidr, community string) ([]Found, error) {
	if community == "" {
		community = "public"
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("bad CIDR %q: %w", cidr, err)
	}
	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	size := ^mask + 1
	if size > 1026 {
		return nil, fmt.Errorf("subnet too large (%d hosts) — use /22 or smaller", size)
	}
	var mu sync.Mutex
	var out []Found
	var wg sync.WaitGroup
	sem := make(chan struct{}, 48)
	for i := uint32(1); i < size-1; i++ { // skip network + broadcast
		ipb := make([]byte, 4)
		binary.BigEndian.PutUint32(ipb, base+i)
		ip := net.IP(ipb).String()
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pr := poller.Ping(ip, 1500*time.Millisecond)
			// Probe SNMP even if ping is filtered — many devices block ICMP.
			sn, sd, snmpOk := poller.Probe(ip, community)
			if pr.AvgMs < 0 && !snmpOk {
				return
			}
			mu.Lock()
			out = append(out, Found{IP: ip, LatencyMs: pr.AvgMs, SysName: sn, SysDescr: sd, SNMP: snmpOk})
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	return out, nil
}
