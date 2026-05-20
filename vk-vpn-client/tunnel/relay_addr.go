package tunnel

import "net"

// RelayAddrUnroutable reports destinations the VPS relay must not dial (link-local, IPv6, unspecified).
func RelayAddrUnroutable(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.To4() == nil {
		return true
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
