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

// SetMTU configures the MTU of the TUN adapter.
func SetMTU(adapterName string, mtu int) error {
	log.Printf("Setting MTU of %s to %d...", adapterName, mtu)
	_, err := runNetsh("interface", "ipv4", "set", "subinterface",
		adapterName, "mtu="+strconv.Itoa(mtu), "store=active")
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
		"stun.l.google.com", "stun.vk.com", "videowebrtc.okcdn.ru",
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

	for _, item := range extraBypassIPs {
		if net.ParseIP(item) != nil {
			if net.ParseIP(item).To4() != nil {
				log.Printf("Adding TURN/STUN bypass route for %s via %s", item, defaultGateway)
				addHostRoute(item, defaultGateway)
			}
			continue
		}
		// It's a hostname (e.g. videowebrtc.okcdn.ru)! Resolve it to IPv4!
		log.Printf("Resolving dynamic bypass hostname: %s", item)
		ips, err := net.LookupIP(item)
		if err != nil {
			log.Printf("Warning: failed to resolve dynamic bypass hostname %s: %v", item, err)
			continue
		}
		for _, ip := range ips {
			if ip.To4() != nil {
				log.Printf("Adding direct route for bypassed hostname %s (%s) via %s", item, ip.String(), defaultGateway)
				addHostRoute(ip.String(), defaultGateway)
			}
		}
	}

	// Add VK IP subnets bypass routes
	vkSubnets := []string{
		"87.240.128.0/18",
		"93.186.224.0/20",
		"95.142.192.0/18",
		"185.32.248.0/22",
		"185.180.200.0/22",
		"217.20.144.0/20",
		"217.20.154.0/23",
		"95.213.0.0/17",
		"79.137.128.0/18",
	}
	for _, subnet := range vkSubnets {
		log.Printf("Adding direct route for VK subnet %s via %s", subnet, defaultGateway)
		AddBypassSubnet(subnet, defaultGateway)
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

// AddBypassSubnet installs a bypass route for a subnet CIDR (e.g. "87.240.128.0/18") with metric 1.
func AddBypassSubnet(cidr, gateway string) {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return
	}
	ip := parts[0]
	ones, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	mask := net.CIDRMask(ones, 32)
	maskIP := net.IP(mask)
	maskStr := maskIP.String()

	cmd := exec.Command("route", "ADD", ip, "MASK", maskStr, gateway, "METRIC", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// retry without METRIC
		cmd2 := exec.Command("route", "ADD", ip, "MASK", maskStr, gateway)
		out, err = cmd2.CombinedOutput()
		if err != nil {
			log.Printf("route ADD subnet %s failed: %s", cidr, strings.TrimSpace(string(out)))
		}
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

// CleanupAllStaleState performs a deep network cleanup to mimic the "reboot effect".
// It flushes old WLVPN routing configurations, bypass subnets, and removes stale virtual adapters.
func CleanupAllStaleState(defaultGateway string) {
	log.Println("Performing deep network cleanup (the 'reboot effect')...")

	// 1. Delete split routes (ignore errors if not present)
	exec.Command("route", "DELETE", "0.0.0.0", "MASK", "128.0.0.0").Run()
	exec.Command("route", "DELETE", "128.0.0.0", "MASK", "128.0.0.0").Run()

	// 2. Delete all possible VK / STUN / TURN bypass subnets
	vkSubnets := []string{
		"87.240.128.0/18",
		"93.186.224.0/20",
		"95.142.192.0/18",
		"185.32.248.0/22",
		"185.180.200.0/22",
		"217.20.144.0/20",
		"217.20.154.0/23",
		"95.213.0.0/17",
		"79.137.128.0/18",
	}
	for _, subnet := range vkSubnets {
		parts := strings.Split(subnet, "/")
		if len(parts) == 2 {
			ip := parts[0]
			ones, _ := strconv.Atoi(parts[1])
			mask := net.CIDRMask(ones, 32)
			maskIP := net.IP(mask)
			exec.Command("route", "DELETE", ip, "MASK", maskIP.String()).Run()
		}
	}

	// 3. Delete DNS bypasses
	for _, dns := range []string{"1.1.1.1", "8.8.8.8", "8.8.4.4", "1.0.0.1"} {
		exec.Command("route", "DELETE", dns, "MASK", "255.255.255.255").Run()
	}

	// 4. Delete bypassed default gateway route
	if defaultGateway != "" {
		exec.Command("route", "DELETE", defaultGateway, "MASK", "255.255.255.255").Run()
	}

	// 5. Clean up any stale WLVPN adapters using powershell, falling back to netsh
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Get-NetAdapter | Where-Object { $_.Name -like '*WLVPN*' } | Remove-NetAdapter -Confirm:$false")
	out, err := cmd.CombinedOutput()
	if err == nil {
		log.Println("Cleaned up stale WLVPN adapters.")
	} else {
		exec.Command("netsh", "interface", "delete", "interface", "name=WLVPN").Run()
		log.Printf("Stale adapter cleanup output (PS failed, fallback to netsh executed): %s", strings.TrimSpace(string(out)))
	}

	log.Println("Network cleanup completed successfully.")
}
