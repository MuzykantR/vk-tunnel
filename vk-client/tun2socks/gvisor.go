package tun2socks

import (
	"fmt"
	"log"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/engine"

	"vk-client/tunconfig"
)

// Engine forwards IP packets from a Wintun adapter to a local SOCKS5 proxy.
type Engine struct {
	adapterName string
	socksURL    string
	mtu         int
	mu          sync.Mutex
	started     bool
}

func NewEngine(adapterName, socksAddr string, mtu int) *Engine {
	if mtu <= 0 {
		mtu = tunconfig.DefaultMTU
	}
	return &Engine{
		adapterName: adapterName,
		socksURL:    "socks5://" + socksAddr,
		mtu:         mtu,
	}
}

func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}
	log.Printf("Starting tun2socks: device=tun://%s proxy=%s mtu=%d", e.adapterName, e.socksURL, e.mtu)
	key := &engine.Key{
		Device: "tun://" + e.adapterName,
		Proxy:  e.socksURL,
		MTU:    e.mtu,
	}
	engine.Insert(key)
	engine.Start()
	e.started = true
	log.Println("tun2socks engine running")
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return
	}
	engine.Stop()
	e.started = false
	log.Println("tun2socks engine stopped")
}

// Legacy constructor kept for compatibility during migration.
func NewEngineFromSession(_ interface{}, socksURL string) *Engine {
	return NewEngine("WLVPN", socksURL, tunconfig.DefaultMTU)
}

func MustStart(adapterName, socksHostPort string) (*Engine, error) {
	eng := NewEngine(adapterName, socksHostPort, tunconfig.MTUFromEnv())
	if err := eng.Start(); err != nil {
		return nil, fmt.Errorf("tun2socks: %w", err)
	}
	return eng, nil
}
