package wintun

import (
	"fmt"
	"log"
	"net"
	"os/exec"
)

// ConfigureInterface assigns an IP to the Wintun adapter via netsh.
func ConfigureInterface(adapterName string, ip string, mask string) error {
	log.Printf("Assigning IP %s/%s to interface '%s'...", ip, mask, adapterName)
	cmd := exec.Command("netsh", "interface", "ipv4", "set", "address",
		fmt.Sprintf("name=%s", adapterName), "static", ip, mask)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to assign IP: %s (Output: %s)", err, string(out))
	}
	return nil
}

// SetupRoutingLoopBypass resolves VK/TURN servers and adds direct routes to bypass the tunnel.
func SetupRoutingLoopBypass(defaultGateway string) error {
	// List of domains or IPs that must bypass the VPN tunnel (direct route).
	vkDomains := []string{
		"vk.com",
		"vk.ru",
		"okcdn.ru",
		"mycdn.me",
		"stun.l.google.com",
		"stun.vk.com",
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
				err := AddRoute(ip.String(), "255.255.255.255", defaultGateway)
				if err != nil {
					log.Printf("Failed to add bypass route for %s: %v", ip.String(), err)
				}
			}
		}
	}
	return nil
}

// AddRoute adds a Windows routing table entry via route.exe.
func AddRoute(destIP, mask, gateway string) error {
	cmd := exec.Command("route", "add", destIP, "mask", mask, gateway)
	return cmd.Run()
}

// RedirectDefaultTraffic modifies 0.0.0.0/0 to point to the Wintun adapter.
func RedirectDefaultTraffic(wintunIP string) error {
	log.Println("Redirecting all traffic to Wintun adapter...")
	// For Windows, often you add two /1 routes (0.0.0.0/1 and 128.0.0.0/1) 
	// to override the default 0.0.0.0/0 without deleting it, making cleanup easier.
	if err := AddRoute("0.0.0.0", "128.0.0.0", wintunIP); err != nil {
		return err
	}
	if err := AddRoute("128.0.0.0", "128.0.0.0", wintunIP); err != nil {
		return err
	}
	return nil
}

// CleanupRouting restores default routing.
func CleanupRouting(wintunIP string) {
	log.Println("Cleaning up routing tables...")
	exec.Command("route", "delete", "0.0.0.0", "mask", "128.0.0.0", wintunIP).Run()
	exec.Command("route", "delete", "128.0.0.0", "mask", "128.0.0.0", wintunIP).Run()
}
