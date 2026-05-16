package webrtc

import (
	"log"

	"github.com/pion/webrtc/v3"
)

type WebRTCClient struct {
	PeerConnection *webrtc.PeerConnection
	DataChannel    *webrtc.DataChannel
}

func NewWebRTCClient() (*WebRTCClient, error) {
	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create a new RTCPeerConnection
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	// Create a DataChannel for control traffic (VPN signaling/auth)
	// We can also create a VP8 track for heavy data traffic later
	dc, err := pc.CreateDataChannel("vpn-control", nil)
	if err != nil {
		return nil, err
	}

	dc.OnOpen(func() {
		log.Printf("Data channel '%s'-'%d' open.\n", dc.Label(), dc.ID())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("Message from DataChannel '%s': '%s'\n", dc.Label(), string(msg.Data))
	})

	return &WebRTCClient{
		PeerConnection: pc,
		DataChannel:    dc,
	}, nil
}

func (c *WebRTCClient) Close() {
	if c.PeerConnection != nil {
		c.PeerConnection.Close()
	}
}
