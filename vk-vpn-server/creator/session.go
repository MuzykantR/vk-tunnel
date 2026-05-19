package creator

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/tunnel"
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
	// inCh receives data the joiner sent for this connection. A small buffer
	// is enough — the writer goroutine just copies to conn.Write without
	// extra processing.
	inCh chan []byte
}

// SessionOpts — tunables from whitelist-bypass headless --resources / join-link obfuscation.
type SessionOpts struct {
	JoinLink  string
	Resources ResourceProfile
}

// TunnelSession is the creator-side WebRTC peer with a negotiated tunnel DataChannel.
type TunnelSession struct {
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	rawDC     datachannel.ReadWriteCloser // non-nil when Detach() succeeded
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	mu        sync.Mutex
	conns     sync.Map
	obf       *tunnel.TunnelObfuscator
	readBuf   int
	maxDCBuf  uint64

	onICE      func(*webrtc.ICECandidate)
	OnCloseReq func()

	closeOnce sync.Once
}

func NewTunnelSession(ice []webrtc.ICEServer, opts SessionOpts) (*TunnelSession, error) {
	// SettingEngine matched to whitelist-bypass:
	//   - DetachDataChannels: relay reads from raw ReadWriteCloser, never
	//     blocks the SCTP reader on a slow consumer.
	//   - SetSCTPMaxReceiveBufferSize: 8 MB so high-BDP links don't throttle.
	//   - EnableSCTPZeroChecksum: ~10% CPU win; DTLS already authenticates.
	se := webrtc.SettingEngine{}
	se.DetachDataChannels()
	se.SetSCTPMaxReceiveBufferSize(8 * 1024 * 1024)
	se.EnableSCTPZeroChecksum(true)
	// Custom API must register codecs explicitly — otherwise AddTrack/CreateOffer
	// fail with "RTPSender created with no codecs" (client joiner does the same).
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me))

	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}
	s := &TunnelSession{
		pc:       pc,
		readBuf:  opts.Resources.ReadBuf,
		maxDCBuf: opts.Resources.MaxDCBuf,
	}
	if s.readBuf <= 0 {
		s.readBuf = 32 * 1024
	}
	if opts.JoinLink != "" {
		if obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(opts.JoinLink)); err == nil {
			s.obf = obf
			log.Printf("[creator] obfuscator on (epoch=0x%08x)", obf.LocalEpoch())
		} else {
			log.Printf("[creator] obfuscator disabled: %v", err)
		}
	}

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

	// Tunnel DataChannel (Negotiated ID: 2). DEFAULTS = ordered + reliable.
	// Detach() + a Go-side read loop is what keeps the SCTP reader unblocked
	// under load. Same model as whitelist-bypass.
	neg := true
	id := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &neg,
		ID:         &id,
	})
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.dc = dc
	if s.maxDCBuf > 0 {
		dc.SetBufferedAmountLowThreshold(s.maxDCBuf)
	}

	dc.OnOpen(func() {
		log.Println("[creator] tunnel DataChannel open — relay bridge active")
		raw, err := dc.Detach()
		if err != nil {
			log.Printf("[creator] dc.Detach failed, falling back to OnMessage: %v", err)
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleDCMessage(msg.Data)
			})
			return
		}
		s.rawDC = raw
		go s.dcReadLoop()
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

// dcReadLoop drains the detached DataChannel into handleDCMessage. Goroutine
// owned by the session; exits when the DC closes or PC is torn down.
func (s *TunnelSession) dcReadLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, isString, err := s.rawDC.ReadDataChannel(buf)
		if err != nil {
			log.Printf("[creator] dc read loop exiting: %v", err)
			return
		}
		if isString || n == 0 {
			continue
		}
		cp := make([]byte, n)
		copy(cp, buf[:n])
		s.handleDCMessage(cp)
	}
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
	s.closeOnce.Do(func() {
		s.closeAllConns()
		if s.rawDC != nil {
			s.rawDC.Close()
		}
		if s.pc != nil {
			s.pc.Close()
		}
	})
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
	if s.obf != nil {
		pt, ok := s.obf.DecryptPayload(data)
		if !ok {
			log.Printf("[dc] decrypt failed, drop %d bytes", len(data))
			return
		}
		data = pt
	}
	if len(data) < 5 {
		return
	}
	connID := binary.BigEndian.Uint32(data[0:4])
	mt := data[4]
	payload := data[5:]

	switch mt {
	case MsgConnect:
		go s.connectTCP(connID, string(payload))
	case MsgUDP:
		go s.handleUDP(connID, payload)
	case MsgData:
		val, ok := s.conns.Load(connID)
		if !ok {
			return
		}
		dc := val.(*dcConn)
		cp := make([]byte, len(payload))
		copy(cp, payload)
		select {
		case dc.inCh <- cp:
		default:
			log.Printf("[dc] conn %d inCh full, dropping %d bytes from joiner", connID, len(payload))
		}
	case MsgClose:
		val, ok := s.conns.LoadAndDelete(connID)
		if ok {
			dc := val.(*dcConn)
			close(dc.inCh)
		}
	}
}

// sendDCFrame writes a framed message into the detached DataChannel. Pion's
// raw ReadWriteCloser is safe for concurrent Write — internally each call
// becomes one SCTP datagram, so we don't need to serialise through a channel.
func (s *TunnelSession) sendDCFrame(connID uint32, mt byte, payload []byte) {
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], connID)
	buf[4] = mt
	copy(buf[5:], payload)
	wire := buf
	if s.obf != nil {
		wire = s.obf.EncryptPayload(buf)
		if wire == nil {
			return
		}
	}
	if s.rawDC != nil {
		if _, err := s.rawDC.Write(wire); err != nil {
			// DC torn down; the connection cleanup will follow shortly.
		}
		return
	}
	if s.dc != nil && s.dc.ReadyState() == webrtc.DataChannelStateOpen {
		s.dc.Send(wire)
	}
}

func (s *TunnelSession) waitDCBackpressure() {
	if s.maxDCBuf == 0 || s.dc == nil {
		return
	}
	for s.dc.BufferedAmount() > s.maxDCBuf {
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *TunnelSession) handleUDP(connID uint32, payload []byte) {
	if len(payload) < 2 {
		return
	}
	addrLen := int(payload[0])
	if len(payload) < 1+addrLen {
		return
	}
	addr := string(payload[1 : 1+addrLen])
	data := payload[1+addrLen:]
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write(data)
	resp := make([]byte, 4096)
	n, err := conn.Read(resp)
	if err != nil {
		return
	}
	s.sendDCFrame(connID, MsgUDPReply, resp[:n])
}

func (s *TunnelSession) connectTCP(connID uint32, addr string) {
	log.Printf("[dc] CONNECT %d -> %s", connID, addr)

	// Reject unroutable IPv6 destinations before we try to dial them — these
	// always fail with "invalid argument" on the VPS and waste a 10 s dial timeout.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				s.sendDCFrame(connID, MsgConnectErr, []byte("unroutable address"))
				return
			}
		}
	}

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Printf("[dc] CONNECT %d failed: %v", connID, err)
		s.sendDCFrame(connID, MsgConnectErr, []byte(err.Error()))
		return
	}
	dc := &dcConn{
		conn: conn,
		inCh: make(chan []byte, 256),
	}
	s.conns.Store(connID, dc)
	s.sendDCFrame(connID, MsgConnectOK, nil)
	log.Printf("[dc] CONNECTED %d -> %s", connID, addr)

	// Writer: joiner → remote TCP (data coming from the client).
	go func() {
		for data := range dc.inCh {
			conn.Write(data)
		}
		conn.Close()
	}()

	// Reader: remote TCP → joiner. Pause reads when DC send buffer is full (WLB max-dc-buf).
	buf := make([]byte, s.readBuf)
	for {
		s.waitDCBackpressure()
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
