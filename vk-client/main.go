package main

import (
	"context"
	"embed"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"vk-client/tun2socks"
	"vk-client/wintun"
	clientAPI "github.com/vk-vpn/client/api"
)

//go:embed all:frontend/dist
var assets embed.FS

// App struct holds the application state.
type App struct {
	tunEngine *tun2socks.Engine
	defaultGW string
	wintunIP  string
}

func NewApp() *App {
	return &App{
		defaultGW: wintun.DefaultGateway(),
		wintunIP:  "10.8.0.2",
	}
}

// Connect is called from the JS frontend when the user clicks "Connect".
func (a *App) Connect(uri string) error {
	log.Printf("Connecting to VPN via URI: %s", uri)

	// 0. Perform deep network cleanup to mimic the "reboot effect"
	wintun.CleanupAllStaleState(a.defaultGW)

	// 1. Start WebRTC client — establishes ICE + DC + SOCKS5 listener
	log.Println("Starting VPN Core Client...")
	bypassIPs, err := clientAPI.StartClient(uri, 1080)
	if err != nil {
		return err
	}

	// 2. Start tun2socks (creates its own Wintun adapter)
	log.Println("Starting tun2socks engine...")
	tunEngine, err := tun2socks.MustStart("WLVPN", "127.0.0.1:1080")
	if err != nil {
		return err
	}
	a.tunEngine = tunEngine

	// 3. Wait for adapter, configure IP (no DNS on TUN — our SOCKS5 lacks UDP relay)
	time.Sleep(1 * time.Second)
	tunName := wintun.FindTunAdapter()
	if tunName == "" {
		tunName = "WLVPN"
	}
	log.Printf("Using TUN adapter: %s", tunName)
	if err := wintun.ConfigureInterface(tunName, a.wintunIP, "255.255.255.0"); err != nil {
		return err
	}
	// Set MTU to 1400 to prevent UDP fragmentation issues over Docker/VPN networks
	if err := wintun.SetMTU(tunName, 1400); err != nil {
		log.Printf("Warning: failed to set MTU: %v", err)
	}

	// 4. Resolve and add bypass routes AFTER the WLVPN adapter is fully configured and settled.
	// This prevents Windows from flushing or invalidating dynamic bypass routes during adapter creation.
	log.Println("Setting up routing bypass for VK SFU nodes...")
	if err := wintun.SetupRoutingLoopBypass(a.defaultGW, bypassIPs); err != nil {
		return err
	}

	// 5. Bypass DNS servers so DNS resolves instantly (not through broken UDP tunnel)
	for _, dns := range []string{"1.1.1.1", "8.8.8.8", "8.8.4.4", "1.0.0.1"} {
		wintun.AddBypassRoute(dns, a.defaultGW)
	}

	// 6. Redirect default traffic through TUN
	if err := wintun.RedirectDefaultTraffic(tunName); err != nil {
		return err
	}

	log.Println("VPN Connected Successfully!")
	return nil
}

// Disconnect restores routing and closes adapters.
func (a *App) Disconnect() {
	log.Println("Disconnecting VPN...")
	wintun.CleanupRouting("WLVPN")
	if a.tunEngine != nil {
		a.tunEngine.Stop()
	}
	log.Println("VPN Disconnected.")
}

func main() {
	// Redirect logs to app.log next to the executable for easy debugging
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