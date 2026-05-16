package api

import (
	"context"
	"fmt"
	"log"

	"github.com/vk-vpn/client/parser"
	"github.com/vk-vpn/client/proxy"
	"github.com/vk-vpn/client/webrtc"
)

var (
	activeClient *webrtc.Client
	cancelFunc   context.CancelFunc
)

// StartClient initializes the VPN client, connects to WebRTC, and starts SOCKS5.
// This function is intended to be exported via CGO/Gomobile.
// Note: In a real CGO environment, you'd use //export StartClient.
func StartClient(uri string, socksPort int) error {
	if activeClient != nil {
		return fmt.Errorf("client is already running")
	}

	log.Printf("StartClient called with URI: %s", uri)

	// 1. Parse and Decrypt URI
	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return fmt.Errorf("failed to parse URI: %w", err)
	}

	log.Printf("Successfully decrypted URI. Link: %s", payload.Link)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel

	// 2. Setup WebRTC Client
	client, err := webrtc.NewClient(ctx, payload)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to init WebRTC: %w", err)
	}
	activeClient = client

	// Connect to VK signaling
	if err := activeClient.Connect(); err != nil {
		cancel()
		return fmt.Errorf("failed to connect WebRTC: %w", err)
	}

	// 3. Start Local SOCKS5 Proxy
	socksServer, err := proxy.NewSOCKS5Server(socksPort, activeClient)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to init SOCKS5: %w", err)
	}

	// Run SOCKS5 in background
	go func() {
		if err := socksServer.Start(); err != nil {
			log.Printf("SOCKS5 Server error: %v", err)
		}
	}()

	return nil
}

// StopClient stops the active VPN client.
// export StopClient
func StopClient() {
	if cancelFunc != nil {
		cancelFunc()
		cancelFunc = nil
	}
	if activeClient != nil {
		activeClient.Close()
		activeClient = nil
		log.Println("VPN Client stopped.")
	}
}
