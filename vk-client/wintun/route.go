package wintun

import (
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

const tunnelPeer = "10.8.0.1" // on-link next-hop (wintun is point-to-point, no ARP needed)

// ConfigureInterface assigns an IP to the TUN adapter via netsh (no gateway).
func ConfigureInterface(adapterName string, ip string, mask string) error {
	log.Printf("Assigning IP %s/%s to interface '%s'...", ip, mask, adapterName)
	_, err := runNetsh("interface", "ipv4", "set", "address",
		"name="+adapterName, "static", ip, mask)
	return err
}

// SetAdapterDNS pins DNS resolvers on the TUN adapter (same as whitelist-bypass).
func SetAdapterDNS(adapterName string, servers []string) {
	if len(servers) == 0 {
		return
	}
	runNetsh("interface", "ipv4", "set", "dnsservers",
		"name="+adapterName, "static", servers[0], "primary", "validate=no")
	for _, s := range servers[1:] {
		runNetsh("interface", "ipv4", "add", "dnsservers",
			"name="+adapterName, s, "validate=no")
	}
	log.Printf("DNS on %s: %v", adapterName, servers)
}

// FindTunAdapter finds the tun2socks adapter name.
func FindTunAdapter() string {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-NetAdapter | Where-Object { $_.InterfaceDescription -like '*tun2socks*' } | Select-Object -First 1).Name`)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name != "" {
		log.Printf("Found tun2socks adapter: '%s'", name)
	}
	return name
}

// SetupRoutingLoopBypass resolves VK/TURN servers and adds /32 bypass routes
// via the original default gateway with METRIC 1 (highest priority).
func SetupRoutingLoopBypass(defaultGateway string, extraBypassIPs []string) error {
	vkDomains := []string{
		"vk.com", "vk.ru", "okcdn.ru", "mycdn.me",
		"stun.l.google.com", "stun.vk.com",
	}

	for _, domain := range vkDomains {
		ips, err := net.LookupIP(domain)
		if err != nil {
			log.Printf("Warning: failed to resolve %s: %v", domain, err)
			continue
		}
		for _, ip := range ips {
			if ip.To4() != nil {
				log.Printf("Adding direct route for %s (%s) via %s", domain, ip.String(), defaultGateway)
				addHostRoute(ip.String(), defaultGateway)
			}
		}
	}

	for _, ip := range extraBypassIPs {
		if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			continue // skip IPv6
		}
		log.Printf("Adding TURN/STUN bypass route for %s via %s", ip, defaultGateway)
		addHostRoute(ip, defaultGateway)
	}

	log.Printf("Adding DNS bypass route for gateway %s", defaultGateway)
	addHostRoute(defaultGateway, defaultGateway)

	return nil
}

// RedirectDefaultTraffic installs split default routes (0.0.0.0/1 + 128.0.0.0/1)
// through the TUN adapter using netsh (same method as whitelist-bypass).
func RedirectDefaultTraffic(adapterName string) error {
	log.Println("Redirecting all traffic to TUN adapter...")
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := addRouteViaAdapter(prefix, adapterName, tunnelPeer, 2); err != nil {
			log.Printf("add default-half %s failed: %v", prefix, err)
			return err
		}
		log.Printf("Added route %s via %s nexthop=%s metric=2", prefix, adapterName, tunnelPeer)
	}
	return nil
}

// CleanupRouting removes split default routes.
func CleanupRouting(adapterName string) {
	log.Println("Cleaning up routing tables...")
	if adapterName == "" {
		adapterName = "WLVPN"
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		runNetsh("interface", "ipv4", "delete", "route",
			"prefix="+prefix, "interface="+adapterName)
	}
}

// --- Low-level helpers (matching whitelist-bypass routes_windows.go) ---

// addRouteViaAdapter uses netsh to add a route bound to a specific adapter.
func addRouteViaAdapter(prefix, adapter, nexthop string, metric int) error {
	_, err := runNetsh("interface", "ipv4", "add", "route",
		"prefix="+prefix, "interface="+adapter,
		"nexthop="+nexthop, "metric="+strconv.Itoa(metric),
		"store=active")
	return err
}

// AddBypassRoute adds a /32 bypass route for ip via gateway (exported for main.go).
func AddBypassRoute(ip, gateway string) {
	if err := addHostRoute(ip, gateway); err != nil {
		log.Printf("DNS bypass route for %s failed: %v", ip, err)
	}
}

// addHostRoute installs a /32 bypass route with metric 1 (highest priority).
func addHostRoute(ip, gateway string) error {
	cmd := exec.Command("route", "ADD", ip, "MASK", "255.255.255.255", gateway, "METRIC", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// retry without METRIC
		cmd2 := exec.Command("route", "ADD", ip, "MASK", "255.255.255.255", gateway)
		out, err = cmd2.CombinedOutput()
		if err != nil {
			log.Printf("route ADD %s failed: %s", ip, strings.TrimSpace(string(out)))
		}
	}
	return err
}

// runNetsh executes netsh with the given args.
func runNetsh(args ...string) ([]byte, error) {
	cmd := exec.Command("netsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("netsh %s failed: %s (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, err
}
