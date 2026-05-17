package creator

import (
	"log"
	"sync"

	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/socks"
	"github.com/vk-vpn/client/tunnel"
)

// TunnelSession is the creator-side WebRTC peer with a negotiated tunnel DataChannel.
type TunnelSession struct {
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	mu        sync.Mutex

	onICE func(*webrtc.ICECandidate)
}

func NewTunnelSession(ice []webrtc.ICEServer) (*TunnelSession, error) {
	se := webrtc.SettingEngine{}
	se.DetachDataChannels()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}
	s := &TunnelSession{pc: pc}

	pc.OnICEConnectionStateChange(func(st webrtc.ICEConnectionState) {
		log.Printf("[creator] ICE: %s", st.String())
	})

	audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "a")
	if audio != nil {
		pc.AddTrack(audio)
	}
	video, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "v")
	if video != nil {
		pc.AddTrack(video)
	}

	ordered := true
	pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	neg := true
	id := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{Negotiated: &neg, ID: &id})
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.dc = dc

	dc.OnOpen(func() {
		log.Println("[creator] tunnel DataChannel open — SOCKS relay to internet")
		dt := tunnel.NewDCTunnel(dc, socks.DCBufSize, log.Printf)
		bridge := tunnel.NewRelayBridge(dt, "creator", log.Printf)
		bridge.MarkReady()
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if s.onICE != nil {
			s.onICE(c)
		}
	})

	log.Printf("[creator] PeerConnection ready (%d ICE servers)", len(ice))
	return s, nil
}

func (s *TunnelSession) CreateOffer() (webrtc.SessionDescription, error) {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return offer, err
	}
	s.pc.SetLocalDescription(offer)
	return offer, nil
}

func (s *TunnelSession) CreateAnswer() (webrtc.SessionDescription, error) {
	ans, err := s.pc.CreateAnswer(nil)
	if err != nil {
		return ans, err
	}
	s.pc.SetLocalDescription(ans)
	return ans, nil
}

func (s *TunnelSession) SetRemoteDescription(t webrtc.SDPType, sdp string) error {
	err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: t, SDP: sdp})
	if err != nil {
		return err
	}
	s.remoteSet = true
	for _, c := range s.pending {
		s.pc.AddICECandidate(c)
	}
	s.pending = nil
	return nil
}

func (s *TunnelSession) AddICECandidate(c webrtc.ICECandidateInit) error {
	if s.remoteSet {
		return s.pc.AddICECandidate(c)
	}
	s.pending = append(s.pending, c)
	return nil
}

func (s *TunnelSession) Close() {
	if s.pc != nil {
		s.pc.Close()
	}
}

func (s *TunnelSession) SetOnICE(fn func(*webrtc.ICECandidate)) {
	s.onICE = fn
}
