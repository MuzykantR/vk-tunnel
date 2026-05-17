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
	cookies      []config.CookieInfo
	mu           sync.RWMutex
	currentLink  string
	serverPubKey string
}

func NewDaemon(cookies []config.CookieInfo) *Daemon {
	return &Daemon{
		cookies:      cookies,
		serverPubKey: "mock_public_key_ed25519",
	}
}

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
	for {
		select {
		case <-ctx.Done():
			log.Println("Daemon stopped by context.")
			return
		default:
			log.Println("Initiating new call session...")
			d.runSession(ctx)
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
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	vkSig := signaling.NewVKSignaling(d.cookies)
	defer vkSig.Close()

	peerID := "2000000001"
	wsURL, rawLink, joinBody, err := vkSig.FetchCallURL(ctx, peerID)
	if err != nil {
		log.Printf("Failed to fetch call URL: %v", err)
		return
	}

	d.setLinkInfo(rawLink)
	log.Printf("New Call created! Active link: %s", rawLink)

	ice := webrtc.ParseICEFromJoin(joinBody)
	host := webrtc.NewHost(ice)
	// Handler must be registered before WS readLoop starts (otherwise early SFU messages are lost).
	vkSig.SetNotificationHandler(host.HandleNotification)

	if err := vkSig.Connect(ctx, wsURL); err != nil {
		log.Printf("Failed to connect to signaling: %v", err)
		return
	}
	host.AttachWS(vkSig.WSConn())

	if err := host.Start(); err != nil {
		log.Printf("Failed to start WebRTC host: %v", err)
		return
	}
	defer host.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-vkSig.EventChan:
			if !ok {
				log.Println("Signaling channel closed.")
				return
			}
			if event["type"] == "topology" {
				mode, _ := event["mode"].(string)
				log.Printf("Topology changed to: %s", mode)
				if mode == "SERVER" {
					log.Println("VK SFU server topology active (expected for join links)")
				}
			}
		}
	}
}
