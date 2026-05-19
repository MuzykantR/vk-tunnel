package wintun

import (
	"net"
	"strings"
)

// AddBypassFromCandidate parses an ICE/SDP candidate line and adds /32 routes for every IPv4 (WLB desktoptun).
func AddBypassFromCandidate(candidate, gateway string) {
	for _, ip := range extractCandidateIPs(candidate) {
		AddBypassRoute(ip.String(), gateway)
	}
}

func extractCandidateIPs(line string) []net.IP {
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "a=") {
		line = line[2:]
	}
	if !strings.HasPrefix(line, "candidate:") {
		return nil
	}
	parts := strings.Fields(line)
	var out []net.IP
	for _, p := range parts {
		ip := net.ParseIP(p)
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, v4)
	}
	return dedupIPs(out)
}

func dedupIPs(ips []net.IP) []net.IP {
	seen := make(map[string]struct{})
	var out []net.IP
	for _, ip := range ips {
		k := ip.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, ip)
	}
	return out
}
