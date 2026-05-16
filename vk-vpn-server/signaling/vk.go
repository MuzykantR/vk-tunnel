package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vk-vpn/server/config"
)

type VKSignaling struct {
	wsConn    *websocket.Conn
	cookies   []config.CookieInfo
	mu        sync.Mutex
	EventChan chan map[string]interface{}
}

func NewVKSignaling(cookies []config.CookieInfo) *VKSignaling {
	return &VKSignaling{
		cookies:   cookies,
		EventChan: make(chan map[string]interface{}, 100),
	}
}

// FetchCallURL mimics fetching the WS signaling URL from VK.
// In a real app, this hits https://vk.com/al_video.php?act=join_call
func (s *VKSignaling) FetchCallURL(ctx context.Context, callID string) (string, error) {
	// MOCK logic: In reality, we use http.Client, set cookies, and parse JSON.
	// For now, we return a fake WS URL.
	log.Printf("Fetching signaling URL for call ID: %s", callID)
	
	// Simulate network delay
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// This is where we'd parse the actual wss://... link from VK's response
	return "wss://signaling.vk.com/fake", nil
}

// Connect establishes the WebSocket connection to VK's signaling server
func (s *VKSignaling) Connect(ctx context.Context, wsURL string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Add cookies to header if necessary
	headers := http.Header{}
	var cookieString string
	for _, c := range s.cookies {
		cookieString += fmt.Sprintf("%s=%s; ", c.Name, c.Value)
	}
	headers.Add("Cookie", cookieString)

	// Since it's a mock URL, dialing will fail, but we include the logic for completeness.
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		log.Printf("WS dial failed (expected for mock URL): %v", err)
	} else {
		s.wsConn = conn
	}
	
	log.Printf("Connected to VK Signaling WS: %s", wsURL)

	// Start reading loop
	go s.readLoop(ctx)
	
	return nil
}

func (s *VKSignaling) readLoop(ctx context.Context) {
	// In reality, we read from s.wsConn.ReadMessage()
	// For the skeleton, we will simulate receiving the topology event and then an error to trigger reconnect
	
	// Simulate successful SFU join
	s.EventChan <- map[string]interface{}{
		"type": "topology",
		"mode": "SFU",
	}

	// Simulate VK falling back to SERVER/P2P after 10 seconds
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
		s.EventChan <- map[string]interface{}{
			"type": "topology",
			"mode": "SERVER",
		}
	}
}

// SendMessage sends JSON to the websocket
func (s *VKSignaling) SendMessage(msg map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if s.wsConn == nil {
		// Mock: just log
		data, _ := json.Marshal(msg)
		log.Printf("WS SEND: %s", string(data))
		return nil
	}

	return s.wsConn.WriteJSON(msg)
}

func (s *VKSignaling) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsConn != nil {
		s.wsConn.Close()
	}
}
