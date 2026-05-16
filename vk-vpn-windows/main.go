package main

import (
	"log"

	"github.com/vk-vpn/windows/tun2socks"
	"github.com/vk-vpn/windows/wintun"
)

// App struct holds the application state.
// In a Wails app, this struct has methods that are bound to the JS frontend.
type App struct {
	wintunAdapter *wintun.Adapter
	defaultGW     string // Discovered default gateway of the PC
	wintunIP      string
}

func NewApp() *App {
	return &App{
		defaultGW: "192.168.1.1", // Mocked, use net/route to find actual GW
		wintunIP:  "10.8.0.2",
	}
}

// Connect is called from the JS frontend when the user clicks "Connect".
func (a *App) Connect(uri string) error {
	log.Printf("Connecting to VPN via URI: %s", uri)

	// 1. Invoke the Core Client (from Step 4) to parse the URI and start the SOCKS5 proxy
	// Since we are compiling in pure Go, we can directly import the `api` or `webrtc` package
	// from `github.com/vk-vpn/client` instead of using CGO.
	// err := clientAPI.StartClient(uri, 1080) ...

	// 2. Initialize Wintun Adapter
	adapter, err := wintun.CreateAdapter("WLVPN")
	if err != nil {
		return err
	}
	a.wintunAdapter = adapter

	if err := a.wintunAdapter.Start(); err != nil {
		return err
	}

	// 3. Configure Wintun Interface IP
	if err := wintun.ConfigureInterface("WLVPN", a.wintunIP, "255.255.255.0"); err != nil {
		return err
	}

	// 4. Setup gvisor Tun2Socks Engine
	// We read raw L3 packets from Wintun, process them through gvisor, and pass TCP/UDP payload to 127.0.0.1:1080
	// For demonstration, we assume we get the wintun session here:
	// engine := tun2socks.NewEngine(session, "127.0.0.1:1080")
	// engine.Start()

	// 5. Setup Routing Bypass (CRITICAL loop prevention)
	log.Println("Setting up routing bypass for VK SFU nodes...")
	if err := wintun.SetupRoutingLoopBypass(a.defaultGW); err != nil {
		return err
	}

	// 6. Redirect 0.0.0.0/0 to Wintun
	if err := wintun.RedirectDefaultTraffic(a.wintunIP); err != nil {
		return err
	}

	log.Println("VPN Connected Successfully!")
	return nil
}

// Disconnect restores routing and closes adapters.
func (a *App) Disconnect() {
	log.Println("Disconnecting VPN...")
	
	wintun.CleanupRouting(a.wintunIP)
	
	if a.wintunAdapter != nil {
		a.wintunAdapter.Stop()
	}

	// clientAPI.StopClient()
	log.Println("VPN Disconnected.")
}

func main() {
	// In a real Wails app, you would start the Wails application here:
	// wails.Run(&options.App{ ... Bind: []interface{}{ app } })
	
	// Mock CLI runner for now:
	app := NewApp()
	_ = app.Connect("myvpn://v1:test:test")
	// app.Disconnect()
}
