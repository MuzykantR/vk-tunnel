package webrtc

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/parser"
)

type Client struct {
	pc  *webrtc.PeerConnection
	dc  *webrtc.DataChannel
	ctx context.Context
}

func NewClient(ctx context.Context, payload *parser.VPNPayload) (*Client, error) {
	log.Printf("Initializing WebRTC Joiner for link: %s", payload.Link)

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("Client ICE Connection State: %s\n", state.String())
	})

	return &Client{
		pc:  pc,
		ctx: ctx,
	}, nil
}

// Connect simulates connecting to VK signaling and establishing the peer connection.
func (c *Client) Connect() error {
	// 1. Fetch WebSocket URL from payload.Link
	// 2. Connect to VK WebSocket
	// 3. Send "join" request
	// 4. Wait for SDP Offer from SFU or send our own Offer depending on VK's topology
	
	log.Println("Mocking VK signaling and WebRTC DataChannel setup...")
	
	// Create DataChannel (or wait for it if VK creates it)
	dc, err := c.pc.CreateDataChannel("vpn-control", nil)
	if err != nil {
		return err
	}
	c.dc = dc

	dc.OnOpen(func() {
		log.Printf("Client DataChannel '%s' is open. VPN tunnel ready.", dc.Label())
	})

	return nil
}

// HandleSOCKS5Conn bridges a local SOCKS5 TCP connection to the WebRTC tunnel.
func (c *Client) HandleSOCKS5Conn(conn net.Conn) {
	// In a real implementation:
	// We wrap the raw TCP stream from SOCKS5 into multiplexed packets (e.g. using smux or custom framing)
	// and send them via c.dc.Send().
	// Data received via dc.OnMessage() is demultiplexed and written back to the appropriate local `conn`.
	
	log.Printf("New local SOCKS5 connection from %s, bridging to WebRTC...", conn.RemoteAddr())

	// Example: just reading and echoing for demonstration
	go func() {
		defer conn.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("SOCKS5 read error: %v", err)
				}
				break
			}
			
			// Mock: send via DataChannel (if it was fully open)
			if c.dc != nil && c.dc.ReadyState() == webrtc.DataChannelStateOpen {
				// We should ideally wrap this with connection ID headers
				_ = c.dc.Send(buf[:n])
			}
		}
	}()
}

func (c *Client) Close() {
	if c.pc != nil {
		c.pc.Close()
	}
}
