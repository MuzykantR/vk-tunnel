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

// ResolveSession parses and decrypts the URI, then performs all VK API requests
// (token, captcha, joinConversationByLink) to produce the AuthParams needed by
// the joiner. The returned bypassIPs cover the API base host plus all TURN/STUN
// servers — they must be /32-routed via the physical gateway BEFORE the joiner
// starts WebRTC. This call MUST happen before tun2socks is installed (because
// it does HTTPS to api.vk.com and may open a browser for captcha).
func ResolveSession(uri string) (*webrtc.AuthParams, []string, error) {
	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return nil, nil, fmt.Errorf("parse URI: %w", err)
	}
	log.Printf("Successfully decrypted URI. Link: %s", payload.Link)

	auth, err := webrtc.ResolveJoinLink(payload.Link)
	if err != nil {
		return nil, nil, fmt.Errorf("VK auth: %w", err)
	}

	return auth, collectBypassIPs(auth), nil
}

// StartJoinerWithAuth wires the joiner up to a callback that receives every
// new ICE candidate IP. The caller (vk-client/main.go) installs a /32 bypass
// route for each IP immediately, BEFORE the candidate is fed to the ICE agent,
// so pion's STUN keepalives never traverse the tunnel.
//
// The joiner runs in a background goroutine. This function blocks until the
// DataChannel is open (Ready) or the 120s timeout fires.
func StartJoinerWithAuth(auth *webrtc.AuthParams, socksPort int, onNewBypassIP func(string)) error {
	if activeJoiner != nil {
		return fmt.Errorf("client is already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel

	joiner := webrtc.NewJoiner(ctx, auth, socksPort)
	if onNewBypassIP != nil {
		joiner.SetOnNewBypassIP(onNewBypassIP)
	}
	activeJoiner = joiner

	go func() {
		if err := joiner.Run(); err != nil {
			log.Printf("Joiner exited: %v", err)
		}
	}()

	select {
	case <-joiner.Ready():
		log.Println("VPN tunnel ready (DC or VP8)")
		return nil
	case <-time.After(120 * time.Second):
		cancel()
		activeJoiner = nil
		return fmt.Errorf("timeout waiting for tunnel DataChannel")
	case <-ctx.Done():
		activeJoiner = nil
		return fmt.Errorf("cancelled")
	}
}

// StartClient is kept for backward compatibility. New code should call
// ResolveSession + StartJoinerWithAuth so tun2socks can be installed in
// between (which is what actually fixes the ICE-disconnect-during-route-ADD bug).
//
// Deprecated: prefer the two-phase API.
func StartClient(uri string, socksPort int) (bypassIPs []string, err error) {
	auth, ips, err := ResolveSession(uri)
	if err != nil {
		return nil, err
	}
	if err := StartJoinerWithAuth(auth, socksPort, nil); err != nil {
		return nil, err
	}
	bypassIPs = ips
	bypassIPs = append(bypassIPs, activeJoiner.GetBypassIPs()...)
	return bypassIPs, nil
}

// collectBypassIPs extracts all IPs from TURN/STUN URLs and the API base URL
// that must be routed directly (not through the tunnel). These are the IPs
// the joiner will use to negotiate ICE — they MUST bypass WLVPN.
func collectBypassIPs(auth *webrtc.AuthParams) []string {
	var ips []string
	seen := make(map[string]bool)

	add := func(host string) {
		if seen[host] {
			return
		}
		seen[host] = true
		if net.ParseIP(host) != nil {
			ips = append(ips, host)
			return
		}
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
		s := turnURL
		if idx := len("turn:"); len(s) > idx && s[:idx] == "turn:" {
			s = s[idx:]
		} else if idx := len("stun:"); len(s) > idx && s[:idx] == "stun:" {
			s = s[idx:]
		}
		if qIdx := strings.Index(s, "?"); qIdx >= 0 {
			s = s[:qIdx]
		}
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

	if auth.APIBaseURL != "" {
		if parsed, err := url.Parse(auth.APIBaseURL); err == nil {
			add(parsed.Hostname())
		}
	}
	if auth.Endpoint != "" {
		if parsed, err := url.Parse(auth.Endpoint); err == nil {
			add(parsed.Hostname())
		}
	}

	return ips
}

// WaitIceStable blocks until ICE has been stable or ctx is cancelled. No fallback redirect.
func WaitIceStable(ctx context.Context) error {
	if activeJoiner == nil {
		return fmt.Errorf("no active joiner")
	}
	select {
	case <-activeJoiner.IceStable():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetActiveBypassIPs returns ICE/TURN/STUN IPs that must bypass the tunnel.
func GetActiveBypassIPs() []string {
	if activeJoiner == nil {
		return nil
	}
	return activeJoiner.GetBypassIPs()
}

// IceStable returns a channel that closes the first time the active joiner's
// ICE agent has been continuously connected/completed for at least 1 second.
// Returns a closed channel if no joiner is active so callers don't block forever.
func IceStable() <-chan struct{} {
	if activeJoiner == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return activeJoiner.IceStable()
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
