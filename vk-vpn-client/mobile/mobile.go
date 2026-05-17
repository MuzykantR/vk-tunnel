package mobile

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/vk-vpn/client/parser"
	"github.com/vk-vpn/client/webrtc"
	// "gvisor.dev/gvisor/pkg/tcpip" -> Used internally for tun2socks
)

var (
	activeClient *webrtc.Client
	cancelFunc   context.CancelFunc
)

// StartVPN is exported to Android via Gomobile.
// fd: The raw File Descriptor of the TUN interface created by Android's VpnService.
// vp8Fps, vp8Batch: Parameters for obfsucation pacing.
func StartVPN(uri string, fd int, vp8Fps int, vp8Batch int) error {
	if activeClient != nil {
		return fmt.Errorf("VPN is already running")
	}

	log.Printf("Starting Android VPN | FD: %d | FPS: %d | Batch: %d", fd, vp8Fps, vp8Batch)

	// 1. Parse URI
	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel

	// 2. Setup WebRTC
	client, err := webrtc.NewClient(ctx, payload, 1080)
	if err != nil {
		cancel()
		return err
	}
	activeClient = client

	// 3. Connect WebRTC (Mock)
	if err := activeClient.Connect(); err != nil {
		cancel()
		return err
	}

	// 4. Wrap the TUN file descriptor for gVisor
	// os.NewFile creates an *os.File from the raw integer FD passed from Kotlin.
	tunFile := os.NewFile(uintptr(fd), "tun0")
	if tunFile == nil {
		cancel()
		return fmt.Errorf("invalid TUN file descriptor")
	}

	// 5. Initialize tun2socks Engine (gvisor)
	// Like in Windows, we create a gVisor stack. But instead of wintun, 
	// the gVisor endpoint reads directly from the `tunFile`.
	// engine := tun2socks.NewEngineWithFile(tunFile, client)
	// engine.Start()

	// 6. Apply VP8 Pacing Settings
	// client.SetVP8Pacing(vp8Fps, vp8Batch)

	log.Println("Android VPN engine started successfully.")
	return nil
}

// StopVPN stops the active VPN connection.
func StopVPN() {
	log.Println("Stopping Android VPN...")
	if cancelFunc != nil {
		cancelFunc()
		cancelFunc = nil
	}
	if activeClient != nil {
		activeClient.Close()
		activeClient = nil
	}
}

// ProtectSocketCallback is an interface that Android (Kotlin) will implement.
// We pass it to Go so Go can ask Android to protect specific sockets.
type ProtectSocketCallback interface {
	ProtectSocket(fd int) bool
}

// SetProtector allows Android to inject the VpnService.protect() logic into Go.
var protector ProtectSocketCallback

func SetProtector(p ProtectSocketCallback) {
	protector = p
}

// In Pion/WebRTC, when setting up the UDP/TCP dialer or listener, 
// we would extract the raw socket FD and call `protector.ProtectSocket(int(fd))`
// to prevent the WebRTC traffic from looping back into the TUN interface.
