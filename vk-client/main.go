package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"vk-client/tun2socks"
	"vk-client/tunconfig"
	"vk-client/wintun"

	clientAPI "github.com/vk-vpn/client/api"
	"github.com/vk-vpn/client/logx"
)

func init() {
	logx.Init()
}

//go:embed all:frontend/dist
var assets embed.FS

const (
	adapterName = "WLVPN"
	tunnelIP    = "10.8.0.2"
	tunnelMask  = "255.255.255.0"
	socksPort   = 1080
)

// App owns the lifecycle of a single VPN connection. All Connect/Disconnect
// transitions are mutually exclusive (mu).
type App struct {
	mu         sync.Mutex
	tunEngine  *tun2socks.Engine
	defaultGW  string
	tunName    string
	connected  bool
	bypassSink func(string) // callback shared with joiner; closed at Disconnect
}

func NewApp() *App {
	return &App{}
}

// Connect runs the full VPN bring-up. The order is deliberate and is the
// fix for the ICE-disconnect-during-route-ADD bug:
//
//   1. Forcefully kill any leftover WLVPN adapter / stale routes from the
//      previous session ("reboot effect" without an actual reboot).
//   2. Read the physical default gateway AFTER the cleanup so we never grab
//      10.8.0.1 from a stale WLVPN.
//   3. Resolve the VK session over the clean network (HTTP, captcha browser
//      window). This produces TURN/STUN IPs that must be bypassed.
//   4. Bring up tun2socks (creates the WLVPN adapter) and install ALL known
//      bypass /32 routes BEFORE WebRTC begins. The routing table is stable
//      before pion sends its first STUN binding.
//   5. Start the joiner. For every new remote ICE candidate, the callback
//      installs a /32 bypass synchronously — so VPS public IP is bypassed
//      the moment its candidate arrives, not 6 seconds later.
//   6. Once the DataChannel is open, redirect the default route to WLVPN.
//      All earlier bypass /32s have METRIC 1 and beat the new 0.0.0.0/1.
func (a *App) Connect(uri string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connected {
		return nil
	}

	log.Printf("Connecting to VPN via URI: %s", uri)

	wintun.RemoveAdapter(adapterName)
	wintun.CleanupAllStaleState("")

	a.defaultGW = wintun.DefaultGateway()
	if a.defaultGW == "" || strings.HasPrefix(a.defaultGW, "10.8.0.") {
		return fmt.Errorf("failed to detect physical default gateway (got %q)", a.defaultGW)
	}
	log.Printf("Physical default gateway: %s", a.defaultGW)

	log.Println("Resolving VK session over clean network...")
	auth, knownBypassIPs, err := clientAPI.ResolveSession(uri)
	if err != nil {
		return err
	}
	log.Printf("Session resolved; %d known bypass IPs from TURN/STUN/API", len(knownBypassIPs))

	log.Println("Starting tun2socks engine...")
	tunEngine, err := tun2socks.MustStart(adapterName, "127.0.0.1:1080")
	if err != nil {
		return err
	}
	a.tunEngine = tunEngine

	time.Sleep(1 * time.Second)
	tunName := wintun.FindTunAdapter()
	if tunName == "" {
		tunName = adapterName
	}
	a.tunName = tunName
	log.Printf("Using TUN adapter: %s", tunName)

	if err := wintun.ConfigureInterface(tunName, tunnelIP, tunnelMask); err != nil {
		a.partialTeardown()
		return err
	}
	mtu := tunconfig.MTUFromEnv()
	log.Printf("TUN MTU=%d (env VK_VPN_MTU, default %d)", mtu, tunconfig.DefaultMTU)
	if err := wintun.SetMTU(tunName, mtu); err != nil {
		log.Printf("Warning: failed to set MTU: %v", err)
	}

	log.Println("Installing bypass /32 routes for all known TURN/STUN/API hosts...")
	if err := wintun.SetupRoutingLoopBypass(a.defaultGW, knownBypassIPs); err != nil {
		a.partialTeardown()
		return err
	}
	for _, dns := range []string{"1.1.1.1", "8.8.8.8", "8.8.4.4", "1.0.0.1"} {
		wintun.AddBypassRoute(dns, a.defaultGW)
	}

	// Give NDIS ~1s to settle after the burst of route changes before pion
	// starts the ICE agent. Cheap insurance, hard to debug if skipped.
	time.Sleep(1 * time.Second)

	onNewBypassIP := func(ipOrCand string) {
		if ipOrCand == "" {
			return
		}
		if strings.Contains(ipOrCand, "candidate:") {
			log.Printf("Dynamic bypass (candidate): %s", ipOrCand)
			wintun.AddBypassFromCandidate(ipOrCand, a.defaultGW)
			return
		}
		log.Printf("Dynamic bypass for new ICE IP: %s", ipOrCand)
		wintun.AddBypassRoute(ipOrCand, a.defaultGW)
	}
	a.bypassSink = onNewBypassIP

	log.Println("Starting VPN Core Client (ICE/DTLS/DC) on prepared routing table...")
	if err := clientAPI.StartJoinerWithAuth(auth, socksPort, onNewBypassIP); err != nil {
		a.partialTeardown()
		return err
	}

	flushBypass := func(label string) {
		for _, ip := range clientAPI.GetActiveBypassIPs() {
			wintun.AddBypassRoute(ip, a.defaultGW)
		}
		for _, ip := range clientAPI.ICEBypassIPs() {
			wintun.AddBypassRoute(ip, a.defaultGW)
		}
		log.Printf("Bypass routes flushed (%s)", label)
	}

	if err := a.installDefaultRouteWhenReady(tunName, flushBypass); err != nil {
		a.partialTeardown()
		return err
	}

	a.connected = true
	log.Println("VPN Connected Successfully!")
	return nil
}

// Disconnect tears the tunnel down in reverse-Connect order and removes EVERY
// /32 / subnet bypass route that was installed during this session. Idempotent.
func (a *App) Disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected && a.tunEngine == nil {
		return
	}
	log.Println("Disconnecting VPN...")

	clientAPI.StopClient()

	if a.tunEngine != nil {
		a.tunEngine.Stop()
		a.tunEngine = nil
	}

	wintun.CleanupRouting(a.tunName)
	wintun.RemoveAdapter(adapterName)

	a.bypassSink = nil
	a.connected = false
	log.Println("VPN Disconnected.")
}

// installDefaultRouteWhenReady redirects only when ICE stats expose peer IPs.
// If redirect breaks ICE, split routes are removed and we retry once after reconnect.
func (a *App) installDefaultRouteWhenReady(tunName string, flushBypass func(string)) error {
	const maxAttempts = 2
	pairCtx, pairCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer pairCancel()
	if err := a.waitICEPairIPs(pairCtx); err != nil {
		return fmt.Errorf("ICE pair IPs not ready: %w", err)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Printf("Default redirect attempt %d/%d (WLB + nominated pair bypass)", attempt, maxAttempts)
		flushBypass("pre-redirect")
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		if !clientAPI.IsICEConnected() {
			return fmt.Errorf("ICE dropped before default redirect")
		}
		if err := wintun.RedirectDefaultTraffic(tunName); err != nil {
			return err
		}
		flushBypass("post-redirect")
		clientAPI.NotifyDefaultRouteActive()
		const postRedirectSoak = 3 * time.Second
		soakEnd := time.Now().Add(postRedirectSoak)
		for time.Now().Before(soakEnd) {
			if !clientAPI.IsICEConnected() {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if clientAPI.IsICEConnected() {
			log.Printf("ICE stable %v after redirect attempt %d", postRedirectSoak, attempt)
			return nil
		}
		log.Printf("ICE lost after redirect attempt %d — rolling back split routes only", attempt)
		wintun.DeleteSplitDefaultRoutes(tunName)
		if attempt >= maxAttempts {
			break
		}
		iceCtx, iceCancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := clientAPI.WaitICEConnected(iceCtx)
		iceCancel()
		if err != nil {
			return fmt.Errorf("ICE did not recover after redirect rollback: %w", err)
		}
		log.Println("ICE recovered — retrying default redirect with updated pair stats")
	}
	return fmt.Errorf("ICE unstable after %d redirect attempts", maxAttempts)
}

func (a *App) waitICEPairIPs(ctx context.Context) error {
	start := time.Now()
	for {
		if ips := clientAPI.ICEBypassIPs(); len(ips) > 0 {
			log.Printf("ICE bypass IPs from stats: %v", ips)
			return nil
		}
		if clientAPI.IsICEConnected() && time.Since(start) >= 3*time.Second {
			log.Println("ICE connected 3s — redirect with candidate bypass (stats may still be empty)")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if !clientAPI.IsICEConnected() {
			return fmt.Errorf("ICE not connected while waiting for pair IPs")
		}
	}
}

// partialTeardown is the recovery path used when Connect bails out mid-way.
// We must remove whatever we already installed before returning the error.
func (a *App) partialTeardown() {
	log.Println("Connect failed — running partial teardown...")
	clientAPI.StopClient()
	if a.tunEngine != nil {
		a.tunEngine.Stop()
		a.tunEngine = nil
	}
	wintun.CleanupRouting(a.tunName)
	wintun.RemoveAdapter(adapterName)
	a.bypassSink = nil
	a.connected = false
}

func main() {
	exe, err := os.Executable()
	if err == nil {
		logFile, err := os.OpenFile(filepath.Join(filepath.Dir(exe), "app.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		}
	}

	app := NewApp()

	err = wails.Run(&options.App{
		Title:  "Partizan VPN",
		Width:  800,
		Height: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup: func(ctx context.Context) {
			log.Println("App Started")
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Fatal(err)
	}
}
