package tun2socks

import (
	"fmt"
	"log"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/engine"
)

// Engine forwards IP packets from a Wintun adapter to a local SOCKS5 proxy.
type Engine struct {
	adapterName string
	socksURL    string
	mu          sync.Mutex
	started     bool
}

func NewEngine(adapterName, socksAddr string) *Engine {
	return &Engine{
		adapterName: adapterName,
		socksURL:    "socks5://" + socksAddr,
	}
}

func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}
	log.Printf("Starting tun2socks: device=tun://%s proxy=%s", e.adapterName, e.socksURL)
	key := &engine.Key{
		Device: "tun://" + e.adapterName,
		Proxy:  e.socksURL,
		MTU:    1400,
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
	return &Engine{adapterName: "WLVPN", socksURL: "socks5://" + socksURL}
}

func MustStart(adapterName, socksHostPort string) (*Engine, error) {
	eng := NewEngine(adapterName, socksHostPort)
	if err := eng.Start(); err != nil {
		return nil, fmt.Errorf("tun2socks: %w", err)
	}
	return eng, nil
}
