package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/vk-vpn/server/config"
	"github.com/vk-vpn/server/signaling"
	"github.com/vk-vpn/server/webrtc"
)

type Daemon struct {
	cookies     []config.CookieInfo
	mu          sync.RWMutex
	currentLink string // The raw VK join link
	serverPubKey string // Ed25519 public key (mock for now)
}

func NewDaemon(cookies []config.CookieInfo) *Daemon {
	return &Daemon{
		cookies:      cookies,
		serverPubKey: "mock_public_key_ed25519", // In reality, generate ed25519 keypair
	}
}

// GetLinkInfo returns the current active link and server public key for the Bot API
func (d *Daemon) GetLinkInfo() (string, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.currentLink, d.serverPubKey
}

func (d *Daemon) setLinkInfo(link string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.currentLink = link
}

func (d *Daemon) Start(ctx context.Context) {
	log.Println("Starting VPN Creator Daemon...")

	// Auto-reconnect loop
	for {
		select {
		case <-ctx.Done():
			log.Println("Daemon stopped by context.")
			return
		default:
			log.Println("Initiating new call session...")
			d.runSession(ctx)
			
			// If session exits (due to error or Topology SERVER), wait a bit before reconnecting
			log.Println("Session ended. Reconnecting in 3 seconds...")
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (d *Daemon) runSession(parentCtx context.Context) {
	// Create a context specifically for this WebRTC session
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	vkSig := signaling.NewVKSignaling(d.cookies)
	defer vkSig.Close()

	// 1. Fetch Call Link and Signaling URL
	// We use a valid chat peer_id (e.g. 2000000001) to create the call
	peerID := "2000000001" 
	wsURL, rawLink, err := vkSig.FetchCallURL(ctx, peerID)
	if err != nil {
		log.Printf("Failed to fetch call URL: %v", err)
		return
	}

	// Update the active link so the API can serve it
	d.setLinkInfo(rawLink)
	log.Printf("New Call created! Active link: %s", rawLink)
	
	// TODO: Notify Bot via Webhook if it was a reconnect (Bot can push to clients)

	// 2. Connect to VK WebSocket
	if err := vkSig.Connect(ctx, wsURL); err != nil {
		log.Printf("Failed to connect to signaling: %v", err)
		return
	}

	// 3. Setup local WebRTC
	wrtc, err := webrtc.NewWebRTCClient()
	if err != nil {
		log.Printf("Failed to setup WebRTC: %v", err)
		return
	}
	defer wrtc.Close()

	// 4. Handle VK Signaling events
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-vkSig.EventChan:
			if !ok {
				log.Println("Signaling channel closed.")
				return
			}
			
			// Handle events
			evtType, _ := event["type"].(string)
			switch evtType {
			case "topology":
				mode, _ := event["mode"].(string)
				log.Printf("Topology changed to: %s", mode)
				if mode == "SERVER" || mode == "P2P" {
					log.Println("FATAL: VK switched to P2P/SERVER mode. SFU is unavailable.")
					log.Println("Graceful shutdown of current session...")
					cancel() // This will break the loop and trigger reconnect in Start()
				}
			// Here we would also handle "offer", "answer", "ice" for WebRTC negotiation
			default:
				// log.Printf("Unhandled event: %s", evtType)
			}
		}
	}
}
