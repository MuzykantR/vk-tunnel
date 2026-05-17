package webrtc

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

// Joiner — headless VK joiner (whitelist-bypass vk_joiner.go), DC tunnel mode.
type Joiner struct {
	auth       *AuthParams
	socksPort  int
	readyCh    chan struct{}
	readyOnce  sync.Once
	ctx        context.Context

	joinResp struct {
		Endpoint   string
		TurnURLs   []string
		TurnUser   string
		TurnCred   string
		StunURLs   []string
	}

	ws           *websocket.Conn
	vkMu         sync.Mutex
	vkSeq        int
	remotePeerID *int64
	pc           *webrtc.PeerConnection
	dc           *webrtc.DataChannel
	remoteSet    bool
	pendingICE   []webrtc.ICECandidateInit
	answerSent   bool
	mu           sync.Mutex
	socksConns   map[uint32]net.Conn
	connIDSeq    uint32
	connIDMu     sync.Mutex
}

func NewJoiner(ctx context.Context, auth *AuthParams, socksPort int) *Joiner {
	return &Joiner{
		auth:       auth,
		socksPort:  socksPort,
		readyCh:    make(chan struct{}),
		ctx:        ctx,
		socksConns: make(map[uint32]net.Conn),
	}
}

func (j *Joiner) Ready() <-chan struct{} { return j.readyCh }

func (j *Joiner) Run() error {
	if err := j.joinCall(); err != nil {
		return err
	}
	j.connectSFU()
	return nil
}

func (j *Joiner) joinCall() error {
	parsed, err := url.Parse(j.auth.APIBaseURL)
	if err != nil {
		return err
	}
	body := url.Values{
		"method":          {"vchat.joinConversationByLink"},
		"session_key":     {j.auth.SessionKey},
		"application_key": {j.auth.ApplicationKey},
		"joinLink":        {j.auth.JoinLink},
		"anonymToken":     {j.auth.AnonymToken},
		"isVideo":         {"true"},
		"isAudio":         {"false"},
		"mediaSettings":   {`{"isAudioEnabled":false,"isVideoEnabled":true,"isScreenSharingEnabled":false}`},
		"format":          {"json"},
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: parsed.Hostname()},
		},
	}
	req, _ := http.NewRequest("POST", j.auth.APIBaseURL, strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("joinConversation: %w", err)
	}
	defer resp.Body.Close()

	var jr struct {
		Endpoint   string `json:"endpoint"`
		TurnServer struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"turn_server"`
		StunServer struct {
			URLs []string `json:"urls"`
		} `json:"stun_server"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return err
	}
	if jr.Endpoint == "" {
		return fmt.Errorf("empty endpoint")
	}
	j.joinResp.Endpoint = jr.Endpoint
	j.joinResp.TurnURLs = jr.TurnServer.URLs
	j.joinResp.TurnUser = jr.TurnServer.Username
	j.joinResp.TurnCred = jr.TurnServer.Credential
	j.joinResp.StunURLs = jr.StunServer.URLs
	log.Printf("Joiner: joined, turn=%v", jr.TurnServer.URLs)
	return nil
}

func (j *Joiner) connectSFU() {
	wsURL := j.joinResp.Endpoint +
		"&platform=WEB&appVersion=" + j.auth.AppVersion +
		"&version=" + j.auth.ProtocolVersion +
		"&device=browser&capabilities=0&clientType=VK&tgt=join"

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		WriteBufferSize:  65536,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}
	header := http.Header{}
	header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	header.Set("Origin", "https://vk.com")

	ws, _, err := dialer.DialContext(j.ctx, wsURL, header)
	if err != nil {
		log.Printf("Joiner: WS failed: %v", err)
		return
	}
	j.ws = ws
	log.Println("Connected to VK WebSocket successfully")

	j.vkSend("update-media-modifiers", map[string]interface{}{
		"mediaModifiers": map[string]interface{}{"denoise": true, "denoiseAnn": true},
	})
	j.vkSend("change-media-settings", map[string]interface{}{
		"mediaSettings": map[string]interface{}{
			"isAudioEnabled": false, "isVideoEnabled": true,
			"isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false,
			"isAudioSharingEnabled": false, "isAnimojiEnabled": false,
		},
	})

	go j.pingLoop()
	j.readLoop()
}

// DC framed protocol constants (compatible with whitelist-bypass)
const (
	msgConnect    byte = 0x01
	msgConnectOK  byte = 0x02
	msgConnectErr byte = 0x03
	msgData       byte = 0x04
	msgClose      byte = 0x05
)

func (j *Joiner) initPC() {
	if j.pc != nil {
		return
	}
	var ice []webrtc.ICEServer
	if len(j.joinResp.StunURLs) > 0 {
		ice = append(ice, webrtc.ICEServer{URLs: j.joinResp.StunURLs})
	}
	if len(j.joinResp.TurnURLs) > 0 {
		urls := append([]string{}, j.joinResp.TurnURLs...)
		urls = append(urls, urls[len(urls)-1]+"?transport=tcp")
		ice = append(ice, webrtc.ICEServer{URLs: urls, Username: j.joinResp.TurnUser, Credential: j.joinResp.TurnCred})
	}

	// Use default NewPeerConnection — it registers all standard codecs (VP8, Opus)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		log.Printf("Joiner: PC failed: %v", err)
		return
	}
	j.pc = pc

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Printf("Client ICE state: %s", s.String())
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("Client connection state: %s", s.String())
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
		return
	}
	j.dc = dc
	dc.OnOpen(func() {
		log.Println("VPN tunnel DataChannel open")
		j.readyOnce.Do(func() { close(j.readyCh) })
		go j.listenSOCKS()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		j.handleDCMessage(msg.Data)
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || j.remotePeerID == nil {
			return
		}
		j.vkSendTransmit(*j.remotePeerID, map[string]interface{}{"candidate": c.ToJSON()})
	})
	log.Println("Joiner: PC ready, waiting for offer")
}

// ---------- DC Relay (joiner side) ----------

func (j *Joiner) sendDCFrame(connID uint32, mt byte, payload []byte) {
	if j.dc == nil {
		return
	}
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], connID)
	buf[4] = mt
	copy(buf[5:], payload)
	j.dc.Send(buf)
}

func (j *Joiner) handleDCMessage(data []byte) {
	if len(data) < 5 {
		return
	}
	connID := binary.BigEndian.Uint32(data[0:4])
	mt := data[4]
	payload := data[5:]

	switch mt {
	case msgConnectOK:
		log.Printf("[socks] conn %d: connect OK", connID)
	case msgConnectErr:
		log.Printf("[socks] conn %d: connect error: %s", connID, string(payload))
	case msgData:
		j.mu.Lock()
		conn := j.socksConns[connID]
		j.mu.Unlock()
		if conn != nil {
			conn.Write(payload)
		}
	case msgClose:
		j.mu.Lock()
		conn := j.socksConns[connID]
		delete(j.socksConns, connID)
		j.mu.Unlock()
		if conn != nil {
			conn.Close()
		}
	}
}

func (j *Joiner) listenSOCKS() {
	addr := fmt.Sprintf("127.0.0.1:%d", j.socksPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("SOCKS5 listen failed: %v", err)
		return
	}
	log.Printf("SOCKS5 proxy listening on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go j.handleSOCKS(conn)
	}
}

func (j *Joiner) handleSOCKS(conn net.Conn) {
	// SOCKS5 handshake
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != 0x05 {
		conn.Close()
		return
	}
	conn.Write([]byte{0x05, 0x00}) // no auth

	n, err = conn.Read(buf)
	if err != nil || n < 7 || buf[1] != 0x01 {
		conn.Close()
		return
	}
	var target string
	switch buf[3] {
	case 0x01: // IPv4
		if n < 10 {
			conn.Close()
			return
		}
		ip := net.IP(buf[4:8])
		port := binary.BigEndian.Uint16(buf[8:10])
		target = fmt.Sprintf("%s:%d", ip.String(), port)
	case 0x03: // Domain
		domainLen := int(buf[4])
		if n < 5+domainLen+2 {
			conn.Close()
			return
		}
		domain := string(buf[5 : 5+domainLen])
		port := binary.BigEndian.Uint16(buf[5+domainLen : 5+domainLen+2])
		target = fmt.Sprintf("%s:%d", domain, port)
	default:
		conn.Close()
		return
	}

	// Send CONNECT to creator via DC
	j.connIDMu.Lock()
	j.connIDSeq++
	connID := j.connIDSeq
	j.connIDMu.Unlock()

	j.mu.Lock()
	j.socksConns[connID] = conn
	j.mu.Unlock()

	j.sendDCFrame(connID, msgConnect, []byte(target))

	// Reply success to SOCKS5 client immediately
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Read from local conn -> send to DC
	rbuf := make([]byte, 32768)
	for {
		n, err := conn.Read(rbuf)
		if n > 0 {
			j.sendDCFrame(connID, msgData, rbuf[:n])
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("[socks] conn %d read error: %v", connID, err)
			}
			break
		}
	}
	j.sendDCFrame(connID, msgClose, nil)
	j.mu.Lock()
	delete(j.socksConns, connID)
	j.mu.Unlock()
}

func (j *Joiner) onRegisteredPeer(pid int64) {
	j.remotePeerID = &pid
	log.Printf("Registered peer ID: %d", pid)
}

func (j *Joiner) onTransmittedData(data map[string]interface{}) {
	if j.pc == nil {
		return
	}
	if cand, ok := data["candidate"]; ok {
		b, _ := json.Marshal(cand)
		var ice webrtc.ICECandidateInit
		if json.Unmarshal(b, &ice) == nil {
			if j.remoteSet {
				j.pc.AddICECandidate(ice)
			} else {
				j.pendingICE = append(j.pendingICE, ice)
			}
		}
	}
	if sdp, ok := data["sdp"].(map[string]interface{}); ok {
		sdpType, _ := sdp["type"].(string)
		sdpStr, _ := sdp["sdp"].(string)
		log.Printf("Remote SDP: %s (answerSent=%v)", sdpType, j.answerSent)
		if sdpType == "answer" {
			j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdpStr})
			j.remoteSet = true
			for _, ice := range j.pendingICE {
				j.pc.AddICECandidate(ice)
			}
			j.pendingICE = nil
		} else if sdpType == "offer" {
			// Only process the first offer — duplicate offers from server cause ICE restarts
			if j.answerSent {
				log.Println("Ignoring duplicate offer (answer already sent)")
				return
			}
			j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpStr})
			j.remoteSet = true
			for _, ice := range j.pendingICE {
				j.pc.AddICECandidate(ice)
			}
			j.pendingICE = nil
			ans, err := j.pc.CreateAnswer(nil)
			if err != nil || j.remotePeerID == nil {
				log.Printf("CreateAnswer: %v", err)
				return
			}
			j.pc.SetLocalDescription(ans)
			j.answerSent = true
			j.vkSendTransmit(*j.remotePeerID, map[string]interface{}{
				"sdp": map[string]interface{}{"type": "answer", "sdp": ans.SDP},
			})
			log.Println("Sent SDP answer to creator")
		}
	}
}

func (j *Joiner) handleVKMessage(raw []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if msg["type"] != "notification" {
		return
	}
	notif, _ := msg["notification"].(string)
	switch notif {
	case "connection":
		if params, ok := msg["conversationParams"].(map[string]interface{}); ok {
			if turn, ok := params["turn"].(map[string]interface{}); ok {
				urlsRaw, _ := turn["urls"].([]interface{})
				for _, u := range urlsRaw {
					if s, ok := u.(string); ok {
						j.joinResp.TurnURLs = append(j.joinResp.TurnURLs, s)
					}
				}
				j.joinResp.TurnUser, _ = turn["username"].(string)
				j.joinResp.TurnCred, _ = turn["credential"].(string)
				log.Printf("Updated TURN servers: %v", j.joinResp.TurnURLs)
			}
		}
		j.initPC()
	case "transmitted-data":
		if pid, ok := parseParticipantID(msg["participantId"]); ok && j.remotePeerID == nil {
			j.onRegisteredPeer(pid)
		}
		if data, _ := msg["data"].(map[string]interface{}); data != nil {
			j.onTransmittedData(data)
		}
	case "registered-peer":
		if pid, ok := parseParticipantID(msg["participantId"]); ok {
			j.onRegisteredPeer(pid)
		}
	}
}

func (j *Joiner) vkSend(command string, extra map[string]interface{}) {
	j.vkMu.Lock()
	defer j.vkMu.Unlock()
	if j.ws == nil {
		return
	}
	j.vkSeq++
	extra["command"] = command
	extra["sequence"] = j.vkSeq
	out, _ := json.Marshal(extra)
	j.ws.WriteMessage(websocket.TextMessage, out)
}

func (j *Joiner) vkSendTransmit(pid int64, data map[string]interface{}) {
	j.vkMu.Lock()
	defer j.vkMu.Unlock()
	if j.ws == nil {
		return
	}
	j.vkSeq++
	dataJSON, _ := json.Marshal(data)
	out := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":%s,"participantType":"USER"}`,
		j.vkSeq, pid, dataJSON)
	j.ws.WriteMessage(websocket.TextMessage, []byte(out))
}

func (j *Joiner) pingLoop() {
	for {
		time.Sleep(15 * time.Second)
		j.vkMu.Lock()
		if j.ws != nil {
			j.ws.WriteMessage(websocket.PingMessage, nil)
		}
		j.vkMu.Unlock()
		select {
		case <-j.ctx.Done():
			return
		default:
		}
	}
}

func (j *Joiner) readLoop() {
	for {
		_, msg, err := j.ws.ReadMessage()
		if err != nil {
			log.Printf("VK WS read error: %v", err)
			return
		}
		if string(msg) == "ping" {
			j.vkMu.Lock()
			j.ws.WriteMessage(websocket.TextMessage, []byte("pong"))
			j.vkMu.Unlock()
			continue
		}
		j.handleVKMessage(msg)
	}
}

func (j *Joiner) Close() {
	if j.pc != nil {
		j.pc.Close()
	}
	if j.ws != nil {
		j.ws.Close()
	}
	StopCaptchaProxy()
}
