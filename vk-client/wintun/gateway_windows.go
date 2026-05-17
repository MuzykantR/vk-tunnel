//go:build windows

package wintun

import (
	"bytes"
	"os/exec"
	"strings"
)

// DefaultGateway returns the IPv4 default gateway (e.g. 192.168.1.1).
func DefaultGateway() string {
	out, err := exec.Command("route", "print", "0.0.0.0").CombinedOutput()
	if err != nil {
		return "192.168.1.1"
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			return fields[2]
		}
	}
	// Fallback: parse "Default Gateway" line from route print
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "0.0.0.0") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) >= 3 && fields[0] == "0.0.0.0" {
				return fields[len(fields)-1]
			}
		}
	}
	// ipconfig fallback
	out, err = exec.Command("ipconfig").CombinedOutput()
	if err == nil {
		lines := bytes.Split(out, []byte("\n"))
		for i, line := range lines {
			if strings.Contains(strings.ToLower(string(line)), "default gateway") {
				if i+1 < len(lines) {
					gw := strings.TrimSpace(string(lines[i+1]))
					if gw != "" && gw != "0.0.0.0" {
						return gw
					}
				}
				parts := strings.Split(string(line), ":")
				if len(parts) >= 2 {
					gw := strings.TrimSpace(parts[len(parts)-1])
					if gw != "" {
						return gw
					}
				}
			}
		}
	}
	return "192.168.1.1"
}
