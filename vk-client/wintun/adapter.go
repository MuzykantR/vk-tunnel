package wintun

import (
	"fmt"
	"log"

	"golang.zx2c4.com/wintun"
)

type Adapter struct {
	ifce           *wintun.Adapter
	session        wintun.Session
	sessionStarted bool
}

func CreateAdapter(name string) (*Adapter, error) {
	log.Printf("Initializing Wintun adapter: %s", name)

	if err := ensureWintunDLL(); err != nil {
		return nil, fmt.Errorf("wintun.dll: %w", err)
	}

	ifce, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create wintun adapter: %w", err)
	}

	log.Printf("Created/opened adapter %s", name)

	return &Adapter{
		ifce: ifce,
	}, nil
}

func (a *Adapter) Start() error {
	session, err := a.ifce.StartSession(0x800000)
	if err != nil {
		return fmt.Errorf("failed to start wintun session: %w", err)
	}
	a.session = session
	a.sessionStarted = true
	log.Println("Wintun session started successfully.")
	return nil
}

func (a *Adapter) Stop() {
	if a.sessionStarted {
		a.session.End()
		a.sessionStarted = false
	}
	if a.ifce != nil {
		a.ifce.Close()
		a.ifce = nil
	}
	log.Println("Wintun adapter stopped.")
}

func (a *Adapter) Session() wintun.Session {
	return a.session
}
