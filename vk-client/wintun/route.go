package wintun

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

const tunnelPeer = "10.8.0.1" // on-link next-hop (wintun is point-to-point, no ARP needed)

// addedRoutes tracks every bypass route we installed so Disconnect can remove all of them.
// Key format: "ip/mask" (e.g. "1.2.3.4/32", "87.240.128.0/18"). Value: gateway used.
var addedRoutes sync.Map

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
	cmd := hiddenCmd("powershell", "-NoProfile", "-Command",
		`if (Get-Command Get-NetAdapter -ErrorAction SilentlyContinue) { Invoke-Expression '(Get-NetAdapter | Where-Object { $_.InterfaceDescription -like ''*tun2socks*'' } | Select-Object -First 1).Name' }`)
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

// CleanupRouting removes split default routes AND every bypass route that was installed
// during this session. Critical for preventing stale state between VPN restarts.
func CleanupRouting(adapterName string) {
	log.Println("Cleaning up routing tables...")
	if adapterName == "" {
		adapterName = "WLVPN"
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		runNetsh("interface", "ipv4", "delete", "route",
			"prefix="+prefix, "interface="+adapterName)
	}
	DeleteAllBypassRoutes()
}

// DeleteAllBypassRoutes removes every /32 host route and every subnet route registered in addedRoutes.
// Idempotent — safe to call multiple times.
func DeleteAllBypassRoutes() {
	count := 0
	addedRoutes.Range(func(k, _ any) bool {
		key := k.(string)
		parts := strings.Split(key, "/")
		if len(parts) != 2 {
			addedRoutes.Delete(k)
			return true
		}
		ip := parts[0]
		ones, err := strconv.Atoi(parts[1])
		if err != nil {
			addedRoutes.Delete(k)
			return true
		}
		var maskStr string
		if ones == 32 {
			maskStr = "255.255.255.255"
		} else {
			maskStr = net.IP(net.CIDRMask(ones, 32)).String()
		}
		_ = hiddenCmd("route", "DELETE", ip, "MASK", maskStr).Run()
		addedRoutes.Delete(k)
		count++
		return true
	})
	if count > 0 {
		log.Printf("Removed %d bypass routes", count)
	}
}

// RemoveAdapter forcefully removes a network adapter using a 3-tier fallback:
//
//  1. PowerShell Remove-NetAdapter (best, requires NetAdapter cmdlet)
//  2. netsh interface delete interface
//  3. pnputil unregister wintun root device (extreme: removes ALL stale Wintun instances)
//
// Idempotent. Logs whichever path succeeded.
func RemoveAdapter(name string) {
	if name == "" {
		return
	}
	log.Printf("Force-removing adapter: %s", name)

	out, err := hiddenCmd("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`if (Get-Command Remove-NetAdapter -ErrorAction SilentlyContinue) { Get-NetAdapter -Name '%s' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false; Write-Output 'OK' }`, name)).CombinedOutput()
	if err == nil && strings.Contains(string(out), "OK") {
		log.Printf("Removed %s via PowerShell", name)
		return
	}

	if cmdErr := hiddenCmd("netsh", "interface", "delete", "interface", "name="+name).Run(); cmdErr == nil {
		log.Printf("Removed %s via netsh", name)
		return
	}

	log.Printf("Adapter %s removal: PowerShell+netsh both failed (output: %s)", name, strings.TrimSpace(string(out)))
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
		log.Printf("Bypass route for %s failed: %v", ip, err)
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

	cmd := hiddenCmd("route", "ADD", ip, "MASK", maskStr, gateway, "METRIC", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmd2 := hiddenCmd("route", "ADD", ip, "MASK", maskStr, gateway)
		out, err = cmd2.CombinedOutput()
		if err != nil {
			log.Printf("route ADD subnet %s failed: %s", cidr, strings.TrimSpace(string(out)))
			return
		}
	}
	addedRoutes.Store(cidr, gateway)
}

// addHostRoute installs a /32 bypass route with metric 1 (highest priority) and registers it for cleanup.
func addHostRoute(ip, gateway string) error {
	if ip == gateway {
		return nil
	}
	cmd := hiddenCmd("route", "ADD", ip, "MASK", "255.255.255.255", gateway, "METRIC", "1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmd2 := hiddenCmd("route", "ADD", ip, "MASK", "255.255.255.255", gateway)
		out, err = cmd2.CombinedOutput()
		if err != nil {
			outStr := strings.TrimSpace(string(out))
			// "The object already exists" — treat as success, register for cleanup
			if strings.Contains(strings.ToLower(outStr), "already exists") {
				addedRoutes.Store(ip+"/32", gateway)
				return nil
			}
			log.Printf("route ADD %s failed: %s", ip, outStr)
			return err
		}
	}
	addedRoutes.Store(ip+"/32", gateway)
	return nil
}

// runNetsh executes netsh with the given args.
func runNetsh(args ...string) ([]byte, error) {
	cmd := hiddenCmd("netsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("netsh %s failed: %s (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, err
}

// CleanupAllStaleState performs a deep network cleanup to mimic the "reboot effect".
// Called at the start of every Connect() to ensure a clean slate.
func CleanupAllStaleState(defaultGateway string) {
	log.Println("Performing deep network cleanup (the 'reboot effect')...")

	// 1. Delete any tracked bypass routes from a previous session that we forgot to clean.
	DeleteAllBypassRoutes()

	// 2. Delete split default routes (in case Disconnect didn't run, e.g. after crash)
	hiddenCmd("route", "DELETE", "0.0.0.0", "MASK", "128.0.0.0").Run()
	hiddenCmd("route", "DELETE", "128.0.0.0", "MASK", "128.0.0.0").Run()

	// 3. Delete known stale subnet routes from previous sessions that we don't currently track.
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
			hiddenCmd("route", "DELETE", ip, "MASK", maskIP.String()).Run()
		}
	}

	// 4. Delete DNS bypass routes from previous sessions
	for _, dns := range []string{"1.1.1.1", "8.8.8.8", "8.8.4.4", "1.0.0.1"} {
		hiddenCmd("route", "DELETE", dns, "MASK", "255.255.255.255").Run()
	}

	// 5. Delete bypass route for default gateway (if it was added)
	if defaultGateway != "" {
		hiddenCmd("route", "DELETE", defaultGateway, "MASK", "255.255.255.255").Run()
	}

	// 6. Force-remove WLVPN adapter. Idempotent — safe if it doesn't exist.
	RemoveAdapter("WLVPN")

	log.Println("Network cleanup completed successfully.")
}
