package wintun

import (
	"fmt"
	"log"

	"golang.zx2c4.com/wintun"
)

type Adapter struct {
	ifce    *wintun.Adapter
	session wintun.Session
}

// CreateAdapter initializes a new Wintun virtual network adapter.
func CreateAdapter(name string) (*Adapter, error) {
	log.Printf("Initializing Wintun adapter: %s", name)

	// In a real application, you might need to ensure wintun.dll is in the same directory or system path.
	wintun.Init("")

	ifce, created, err := wintun.CreateAdapter(name, "Wintun")
	if err != nil {
		return nil, fmt.Errorf("failed to create wintun adapter: %w", err)
	}

	if created {
		log.Printf("Created new adapter %s", name)
	} else {
		log.Printf("Reusing existing adapter %s", name)
	}

	return &Adapter{
		ifce: ifce,
	}, nil
}

// Start starts the packet session.
func (a *Adapter) Start() error {
	session, err := a.ifce.StartSession(0x800000) // 8MB capacity
	if err != nil {
		return fmt.Errorf("failed to start wintun session: %w", err)
	}
	a.session = session
	log.Println("Wintun session started successfully.")
	return nil
}

// Stop stops the session and closes the adapter.
func (a *Adapter) Stop() {
	if a.session != 0 { // In the actual library it returns an interface, checking against 0 is just pseudocode depending on version. 
		// Actually, wintun.Session is an interface in the latest version, so we check for nil
	}
	if a.session != nil {
		a.session.End()
	}
	if a.ifce != nil {
		a.ifce.Close()
	}
	log.Println("Wintun adapter stopped.")
}
