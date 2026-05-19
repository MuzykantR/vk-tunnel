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
	var call *signaling.CallSession

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		vkSig := signaling.NewVKSignaling(d.cookies)
		var wsURL string
		var joinBody []byte

		if call == nil {
			log.Println("Initiating new call session (calls.start)...")
			var vkLink, okJoin string
			var err error
			wsURL, vkLink, okJoin, joinBody, err = vkSig.FetchCallURL(ctx, "2000000001")
			if err != nil {
				log.Printf("Failed to fetch call URL: %v", err)
				vkSig.Close()
				sleep(ctx, 3*time.Second)
				continue
			}
			call = &signaling.CallSession{VKJoinLink: vkLink, OKJoinLink: okJoin}
			d.setLinkInfo(vkLink)
			log.Printf("New Call created! Active link: %s", vkLink)
		} else {
			log.Println("Rejoining existing call (no calls.start)...")
			var err error
			wsURL, joinBody, err = vkSig.RejoinConversation(ctx, call.OKJoinLink)
			if err != nil {
				log.Printf("Rejoin failed, will create new call next: %v", err)
				call = nil
				vkSig.Close()
				sleep(ctx, 3*time.Second)
				continue
			}
		}

		d.runBridge(ctx, wsURL, joinBody, call)
		vkSig.Close()

		log.Println("Creator bridge ended — rejoin same call in 3s (WLB model)")
		sleep(ctx, 3*time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

func (d *Daemon) runBridge(ctx context.Context, wsURL string, joinBody []byte, call *signaling.CallSession) {
	ice := webrtc.ParseICEFromJoin(joinBody)
	sessOpts := creator.SessionOpts{
		JoinLink:  call.VKJoinLink,
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
