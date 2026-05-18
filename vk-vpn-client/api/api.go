package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/vk-vpn/client/parser"
	"github.com/vk-vpn/client/webrtc"
)

var (
	activeJoiner *webrtc.Joiner
	cancelFunc   context.CancelFunc
)

// BypassIPs are the IPs that must NOT go through the VPN tunnel (TURN relays, WS endpoints).
func StartClient(uri string, socksPort int) (bypassIPs []string, err error) {
	if activeJoiner != nil {
		return nil, fmt.Errorf("client is already running")
	}

	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}
	log.Printf("Successfully decrypted URI. Link: %s", payload.Link)

	auth, err := webrtc.ResolveJoinLink(payload.Link)
	if err != nil {
		return nil, fmt.Errorf("VK auth: %w", err)
	}

	// Collect TURN/STUN IPs that must bypass the VPN tunnel
	bypassIPs = collectBypassIPs(auth)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel

	joiner := webrtc.NewJoiner(ctx, auth, socksPort)
	activeJoiner = joiner

	go func() {
		if err := joiner.Run(); err != nil {
			log.Printf("Joiner exited: %v", err)
		}
	}()

	select {
	case <-joiner.Ready():
		log.Println("VPN tunnel DataChannel ready")
		// Merge live session IPs (WS endpoint, actual TURN) with initial auth IPs
		bypassIPs = append(bypassIPs, joiner.GetBypassIPs()...)
		return bypassIPs, nil
	case <-time.After(120 * time.Second):
		cancel()
		activeJoiner = nil
		return nil, fmt.Errorf("timeout waiting for tunnel DataChannel")
	case <-ctx.Done():
		return nil, fmt.Errorf("cancelled")
	}
}

// collectBypassIPs extracts all IPs from TURN/STUN URLs and the API base URL
// that must be routed directly (not through the tunnel).
func collectBypassIPs(auth *webrtc.AuthParams) []string {
	var ips []string
	seen := make(map[string]bool)

	add := func(host string) {
		if seen[host] {
			return
		}
		seen[host] = true
		// If it's already an IP, use directly
		if net.ParseIP(host) != nil {
			ips = append(ips, host)
			return
		}
		// Resolve hostname
		resolved, err := net.LookupIP(host)
		if err != nil {
			log.Printf("Warning: failed to resolve %s: %v", host, err)
			return
		}
		for _, ip := range resolved {
			if ip.To4() != nil {
				ips = append(ips, ip.String())
			}
		}
	}

	extractHost := func(turnURL string) string {
		// turn:1.2.3.4:19302 or turn:host.com:19302?transport=tcp
		s := turnURL
		if idx := len("turn:"); len(s) > idx && s[:idx] == "turn:" {
			s = s[idx:]
		} else if idx := len("stun:"); len(s) > idx && s[:idx] == "stun:" {
			s = s[idx:]
		}
		// Remove ?transport=...
		if qIdx := strings.Index(s, "?"); qIdx >= 0 {
			s = s[:qIdx]
		}
		// Remove port
		host, _, err := net.SplitHostPort(s)
		if err != nil {
			return s
		}
		return host
	}

	for _, u := range auth.TurnURLs {
		add(extractHost(u))
	}
	for _, u := range auth.StunURLs {
		add(extractHost(u))
	}

	// API base URL host (ok.ru CDN)
	if auth.APIBaseURL != "" {
		if parsed, err := url.Parse(auth.APIBaseURL); err == nil {
			add(parsed.Hostname())
		}
	}

	return ips
}

func StopClient() {
	if cancelFunc != nil {
		cancelFunc()
		cancelFunc = nil
	}
	if activeJoiner != nil {
		activeJoiner.Close()
		activeJoiner = nil
		log.Println("VPN Client stopped.")
	}
}
