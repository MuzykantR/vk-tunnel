//go:build windows

package wintun

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
)

// DefaultGateway returns the IPv4 default gateway of the physical interface
// (e.g. 192.168.1.1), reliably excluding stale TUN/WLVPN/Wintun adapters
// from previous sessions. Falls back to legacy parsing if PowerShell is unavailable.
func DefaultGateway() string {
	if gw := defaultGatewayFromPowerShell(); gw != "" {
		return gw
	}
	return defaultGatewayLegacy()
}

// psRoute is the JSON shape produced by Get-NetRoute | Select-Object.
type psRoute struct {
	NextHop        string `json:"NextHop"`
	InterfaceAlias string `json:"InterfaceAlias"`
	RouteMetric    int    `json:"RouteMetric"`
}

// defaultGatewayFromPowerShell asks Windows for ALL default routes,
// filters out anything that points through a VPN/TUN adapter, and returns
// the lowest-metric remaining one. This is how whitelist-bypass handles it.
func defaultGatewayFromPowerShell() string {
	out, err := hiddenCmd("powershell", "-NoProfile", "-Command",
		`Get-NetRoute -DestinationPrefix '0.0.0.0/0' -AddressFamily IPv4 -ErrorAction Stop |`+
			`Sort-Object -Property RouteMetric |`+
			`Select-Object NextHop,InterfaceAlias,RouteMetric |`+
			`ConvertTo-Json -Compress`).Output()
	if err != nil {
		return ""
	}
	body := bytes.TrimSpace(out)
	if len(body) == 0 {
		return ""
	}
	// Get-NetRoute returns either an array or a single object — normalize to array
	if body[0] == '{' {
		body = append([]byte{'['}, append(body, ']')...)
	}
	var rows []psRoute
	if err := json.Unmarshal(body, &rows); err != nil {
		return ""
	}
	for _, r := range rows {
		if r.NextHop == "" || r.NextHop == "0.0.0.0" {
			continue
		}
		alias := strings.ToLower(r.InterfaceAlias)
		if isVirtualInterface(alias) {
			continue
		}
		// 10.8.0.0/24 is our tunnel subnet — reject any next-hop in it
		if strings.HasPrefix(r.NextHop, "10.8.0.") {
			continue
		}
		log.Printf("DefaultGateway: %s via %s (metric=%d)", r.NextHop, r.InterfaceAlias, r.RouteMetric)
		return r.NextHop
	}
	return ""
}

// isVirtualInterface returns true if the alias looks like one of our TUN/VPN
// adapters, or a known virtual switch/loopback. The match is case-insensitive
// because the caller already lowercases the alias.
func isVirtualInterface(aliasLower string) bool {
	keywords := []string{
		"wlvpn", "wintun", "tun ", "tap", "loopback",
		"hyper-v", "veth", "vpn", "virtual", "host-only",
		"virtualbox", "vmware",
	}
	for _, kw := range keywords {
		if strings.Contains(aliasLower, kw) {
			return true
		}
	}
	return false
}

// defaultGatewayLegacy is the original `route print 0.0.0.0` parser, kept as
// a fallback for hosts where PowerShell Get-NetRoute is unavailable
// (very old Windows or stripped images).
func defaultGatewayLegacy() string {
	out, err := hiddenCmd("route", "print", "0.0.0.0").CombinedOutput()
	if err != nil {
		return "192.168.1.1"
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			gw := fields[2]
			// Reject tunnel gateway
			if strings.HasPrefix(gw, "10.8.0.") {
				continue
			}
			return gw
		}
	}
	out, err = hiddenCmd("ipconfig").CombinedOutput()
	if err == nil {
		lines := bytes.Split(out, []byte("\n"))
		for i, line := range lines {
			if strings.Contains(strings.ToLower(string(line)), "default gateway") {
				if i+1 < len(lines) {
					gw := strings.TrimSpace(string(lines[i+1]))
					if gw != "" && gw != "0.0.0.0" && !strings.HasPrefix(gw, "10.8.0.") {
						return gw
					}
				}
				parts := strings.Split(string(line), ":")
				if len(parts) >= 2 {
					gw := strings.TrimSpace(parts[len(parts)-1])
					if gw != "" && !strings.HasPrefix(gw, "10.8.0.") {
						return gw
					}
				}
			}
		}
	}
	return "192.168.1.1"
}
