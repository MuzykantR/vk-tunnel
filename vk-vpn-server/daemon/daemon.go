package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/vk-vpn/server/config"
	"github.com/vk-vpn/server/creator"
	"github.com/vk-vpn/server/signaling"
	"github.com/vk-vpn/server/webrtc"
)

type Daemon struct {
	cookies      []config.CookieInfo
	mu           sync.RWMutex
	currentLink  string
	serverPubKey string
	resources    creator.ResourceProfile
}

func NewDaemon(cookies []config.CookieInfo, resources creator.ResourceProfile) *Daemon {
	return &Daemon{
		cookies:      cookies,
		serverPubKey: "mock_public_key_ed25519",
		resources:    resources,
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
			return
		default:
			d.runSession(ctx)
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (d *Daemon) runSession(ctx context.Context) {
	log.Println("Initiating new call session...")

	vkSig := signaling.NewVKSignaling(d.cookies)
	defer vkSig.Close()

	wsURL, rawLink, joinBody, err := vkSig.FetchCallURL(ctx, "2000000001")
	if err != nil {
		log.Printf("Failed to fetch call URL: %v", err)
		return
	}
	d.setLinkInfo(rawLink)
	log.Printf("New Call created! Active link: %s", rawLink)

	ice := webrtc.ParseICEFromJoin(joinBody)
	sessOpts := creator.SessionOpts{
		JoinLink:  rawLink,
		Resources: d.resources,
	}
	bridge, err := creator.NewBridge(wsURL, ice, "1.1", "6", sessOpts)
	if err != nil {
		log.Printf("Failed to create creator bridge: %v", err)
		return
	}
	defer bridge.Close()

	if err := bridge.Connect(); err != nil {
		log.Printf("Failed to connect WS: %v", err)
		return
	}

	log.Println("Creator bridge running (whitelist-bypass DIRECT P2P mode)")

	done := make(chan struct{})
	go func() {
		bridge.Run()
		close(done)
	}()

	select {
	case <-ctx.Done():
		bridge.Close()
	case <-done:
		log.Println("Creator bridge WS closed")
	}
}
