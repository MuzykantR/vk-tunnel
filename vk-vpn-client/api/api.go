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
	activeJoiner *webrtc.Joiner
	cancelFunc   context.CancelFunc
)

func StartClient(uri string, socksPort int) error {
	if activeJoiner != nil {
		return fmt.Errorf("client is already running")
	}

	payload, err := parser.ParseAndDecryptURI(uri)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	log.Printf("Successfully decrypted URI. Link: %s", payload.Link)

	auth, err := webrtc.ResolveJoinLink(payload.Link)
	if err != nil {
		return fmt.Errorf("VK auth: %w", err)
	}

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
		return nil
	case <-time.After(120 * time.Second):
		cancel()
		activeJoiner = nil
		return fmt.Errorf("timeout waiting for tunnel DataChannel")
	case <-ctx.Done():
		return fmt.Errorf("cancelled")
	}
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
