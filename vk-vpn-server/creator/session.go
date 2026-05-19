package creator

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)



// Message types for the framed DC protocol (compatible with whitelist-bypass)
const (
	MsgConnect    byte = 0x01
	MsgConnectOK  byte = 0x02
	MsgConnectErr byte = 0x03
	MsgData       byte = 0x04
	MsgClose      byte = 0x05
	MsgUDP        byte = 0x06
	MsgUDPReply   byte = 0x07
)

type dcConn struct {
	conn net.Conn
	ch   chan []byte
}

// TunnelSession is the creator-side WebRTC peer with a negotiated tunnel DataChannel.
type TunnelSession struct {
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	mu        sync.Mutex
	dcMu      sync.Mutex
	conns     sync.Map

	onICE      func(*webrtc.ICECandidate)
	OnCloseReq func()
}

func NewTunnelSession(ice []webrtc.ICEServer) (*TunnelSession, error) {
	// Use default MediaEngine (registers Opus, VP8 etc.) — no custom API needed
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}
	s := &TunnelSession{pc: pc}

	pc.OnICEConnectionStateChange(func(st webrtc.ICEConnectionState) {
		log.Printf("[creator] ICE: %s", st.String())
	})

	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("[creator] Connection: %s", st.String())
		if st == webrtc.PeerConnectionStateFailed {
			if s.OnCloseReq != nil {
				s.OnCloseReq()
			}
		}
	})

	// Audio + Video tracks (required by VK for call validity)
	audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "a")
	if audio != nil {
		pc.AddTrack(audio)
	}
	video, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "v")
	if video != nil {
		pc.AddTrack(video)
	}

	// VK-specific DataChannels
	ordered := true
	pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	// Tunnel DataChannel (Negotiated ID: 2)
	neg := true
	id := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{Negotiated: &neg, ID: &id})
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.dc = dc

	dc.OnOpen(func() {
		log.Println("[creator] tunnel DataChannel open — relay bridge active")
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		s.handleDCMessage(msg.Data)
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
	s.closeAllConns()
	if s.pc != nil {
		s.pc.Close()
	}
}

func (s *TunnelSession) SetOnICE(fn func(*webrtc.ICECandidate)) {
	s.onICE = fn
}

// SignalingState reports the current pion signaling state so callers can
// distinguish between "ready to accept new offer" and "renegotiation in flight".
func (s *TunnelSession) SignalingState() webrtc.SignalingState {
	if s.pc == nil {
		return webrtc.SignalingStateClosed
	}
	return s.pc.SignalingState()
}

// ---------- DC Relay (framed protocol, compatible with whitelist-bypass) ----------

func (s *TunnelSession) handleDCMessage(data []byte) {
	if len(data) < 5 {
		return
	}
	connID := binary.BigEndian.Uint32(data[0:4])
	mt := data[4]
	payload := data[5:]

	switch mt {
	case MsgConnect:
		go s.connectTCP(connID, string(payload))
	case MsgData:
		val, ok := s.conns.Load(connID)
		if ok {
			dc := val.(*dcConn)
			cp := make([]byte, len(payload))
			copy(cp, payload)
			select {
			case dc.ch <- cp:
			default:
				log.Printf("[dc] conn %d write queue full, dropping %d bytes", connID, len(payload))
			}
		}
	case MsgClose:
		val, ok := s.conns.LoadAndDelete(connID)
		if ok {
			dc := val.(*dcConn)
			close(dc.ch)
		}
	}
}

func (s *TunnelSession) sendDCFrame(connID uint32, mt byte, payload []byte) {
	s.dcMu.Lock()
	defer s.dcMu.Unlock()
	if s.dc == nil {
		return
	}
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], connID)
	buf[4] = mt
	copy(buf[5:], payload)
	s.dc.Send(buf)
}

func (s *TunnelSession) connectTCP(connID uint32, addr string) {
	log.Printf("[dc] CONNECT %d -> %s", connID, addr)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Printf("[dc] CONNECT %d failed: %v", connID, err)
		s.sendDCFrame(connID, MsgConnectErr, []byte(err.Error()))
		return
	}
	dc := &dcConn{conn: conn, ch: make(chan []byte, 256)}
	s.conns.Store(connID, dc)
	s.sendDCFrame(connID, MsgConnectOK, nil)
	log.Printf("[dc] CONNECTED %d -> %s", connID, addr)

	// Writer goroutine
	go func() {
		for data := range dc.ch {
			conn.Write(data)
		}
		conn.Close()
	}()

	// Reader goroutine
	buf := make([]byte, 32768)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			s.sendDCFrame(connID, MsgData, buf[:n])
		}
		if err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("[dc] conn %d read error: %v", connID, err)
			}
			break
		}
	}
	log.Printf("[dc] conn %d closed", connID)
	s.sendDCFrame(connID, MsgClose, nil)
	s.conns.Delete(connID)
}

func (s *TunnelSession) closeAllConns() {
	s.conns.Range(func(key, val any) bool {
		dc := val.(*dcConn)
		dc.conn.Close()
		s.conns.Delete(key)
		return true
	})
}
