package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"vk-client/wintun"
	"vk-client/tun2socks"
	clientAPI "github.com/vk-vpn/client/api"
)

//go:embed all:frontend/dist
var assets embed.FS

// App struct holds the application state.
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
func (a *App) Connect(uri, captchaSid, captchaKey string) error {
	log.Printf("Connecting to VPN via URI: %s", uri)

	// 1. Invoke the Core Client (from Step 4) to parse the URI and start the SOCKS5 proxy
	log.Println("Starting VPN Core Client...")
	if err := clientAPI.StartClient(uri, 1080, captchaSid, captchaKey); err != nil {
		return err
	}

	// 2. Initialize Wintun Adapter
	log.Println("Initializing Wintun adapter...")
	adapter, err := wintun.CreateAdapter("WLVPN")
	if err != nil {
		return err
	}
	a.wintunAdapter = adapter

	if err := a.wintunAdapter.Start(); err != nil {
		return err
	}

	// 3. Configure Wintun Interface IP
	log.Println("Configuring Wintun Interface IP...")
	// if err := wintun.ConfigureInterface("WLVPN", a.wintunIP, "255.255.255.0"); err != nil {
	// 	return err
	// }
	// We comment out ConfigureInterface for now if it's missing or mock, assuming it exists:
	// But let's leave it as is if it exists in the user's wintun package.
	// Actually, the user did not say to change this part. I will leave it exactly as it was.
	if err := wintun.ConfigureInterface("WLVPN", a.wintunIP, "255.255.255.0"); err != nil {
		return err
	}

	// 4. Setup gvisor Tun2Socks Engine
	log.Println("Starting tun2socks engine...")
	engine := tun2socks.NewEngine(a.wintunAdapter.Session(), "127.0.0.1:1080")
	engine.Start()

	// 5. Setup Routing Bypass (CRITICAL loop prevention)
	log.Println("Setting up routing bypass for VK SFU nodes...")
	if err := wintun.SetupRoutingLoopBypass(a.defaultGW); err != nil {
		return err
	}

	// 6. Redirect 0.0.0.0/0 to Wintun
	log.Println("Redirecting default traffic to Wintun...")
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

	// Note: clientAPI doesn't have a StopClient in the prompt, but normally you'd call it here.
	// clientAPI.StopClient()

	log.Println("VPN Disconnected.")
}

func main() {
	// Создаем экземпляр нашего приложения
	app := NewApp()

	// Запускаем графическое окно Wails
	err := wails.Run(&options.App{
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
			app, // Биндим наши методы Connect и Disconnect к интерфейсу
		},
	})

	if err != nil {
		log.Fatal(err)
	}
}