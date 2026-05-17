package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/vk-vpn/client/parser"
	"github.com/vk-vpn/client/webrtc"
)

var (
	activeClient *webrtc.Client
	cancelFunc   context.CancelFunc
)

// StartClient connects to VK SFU, waits for the tunnel DataChannel, then exposes SOCKS5.
func StartClient(uri string, socksPort int) error {
	if activeClient != nil {
		return fmt.Errorf("client is already running")
	}

	log.Printf("StartClient called with URI: %s", uri)

	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return fmt.Errorf("failed to parse URI: %w", err)
	}
	log.Printf("Successfully decrypted URI. Link: %s", payload.Link)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel

	client, err := webrtc.NewClient(ctx, payload, socksPort)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to init WebRTC: %w", err)
	}
	activeClient = client

	if err := client.Connect(); err != nil {
		cancel()
		activeClient = nil
		return fmt.Errorf("failed to connect WebRTC: %w", err)
	}

	select {
	case <-client.Ready():
		log.Println("VPN tunnel DataChannel ready, SOCKS5 relay active")
		return nil
	case <-time.After(120 * time.Second):
		cancel()
		activeClient = nil
		return fmt.Errorf("timeout waiting for tunnel DataChannel (WebRTC may not have connected)")
	case <-ctx.Done():
		return fmt.Errorf("client cancelled")
	}
}

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
