package webrtc

import (
	"log"

	"github.com/pion/webrtc/v3"
)

type WebRTCClient struct {
	PeerConnection *webrtc.PeerConnection
	DataChannel    *webrtc.DataChannel
	VideoTrack     *webrtc.TrackLocalStaticSample
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
	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	// Add dummy audio track required by VK
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "tunnel-audio",
	)
	if err == nil {
		pc.AddTrack(audioTrack)
	}

	// Add VP8 video track for the main tunnel payload
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video", "tunnel-video",
	)
	if err == nil {
		pc.AddTrack(videoTrack)
	}

	// Data Channels specific for VK SFU or Direct
	ordered := true
	pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	// Create a DataChannel for control traffic (VPN signaling/auth)
	// We use negotiated ID 2 for tunnel DC as in whitelist-bypass
	negotiated := true
	dcID := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &dcID,
	})
	if err != nil {
		return nil, err
	}

	dc.OnOpen(func() {
		log.Printf("Data channel '%s'-'%d' open.\n", dc.Label(), *dc.ID())
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		log.Printf("Message from DataChannel '%s': '%d bytes'\n", dc.Label(), len(msg.Data))
	})

	return &WebRTCClient{
		PeerConnection: pc,
		DataChannel:    dc,
		VideoTrack:     videoTrack,
	}, nil
}

func (c *WebRTCClient) Close() {
	if c.PeerConnection != nil {
		c.PeerConnection.Close()
	}
}
