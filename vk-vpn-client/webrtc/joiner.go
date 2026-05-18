package webrtc

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	remoteIPs    map[string]bool // IPs from remote ICE candidates (VPS IP for P2P bypass)
	mu           sync.Mutex
	socksConns   map[uint32]net.Conn
	connectCh    map[uint32]chan error // signals ConnectOK/ConnectErr per conn
	connIDSeq    uint32
	connIDMu     sync.Mutex
	udpListener  *net.UDPConn
}

func NewJoiner(ctx context.Context, auth *AuthParams, socksPort int) *Joiner {
	return &Joiner{
		auth:       auth,
		socksPort:  socksPort,
		readyCh:    make(chan struct{}),
		ctx:        ctx,
		socksConns: make(map[uint32]net.Conn),
		connectCh:  make(map[uint32]chan error),
		remoteIPs:  make(map[string]bool),
	}
}

func (j *Joiner) Ready() <-chan struct{} { return j.readyCh }

// GetBypassIPs returns all IPs that the joiner actively connects to.
// These MUST be routed directly (not through the VPN tunnel) to prevent routing loops.
func (j *Joiner) GetBypassIPs() []string {
	seen := make(map[string]bool)
	var ips []string
	add := func(host string) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		ips = append(ips, host)
		resolved, err := net.LookupIP(host)
		if err != nil {
			return
		}
		for _, ip := range resolved {
			if ip.To4() != nil && !seen[ip.String()] {
				seen[ip.String()] = true
				ips = append(ips, ip.String())
			}
		}
	}

	// 1. WebSocket signaling endpoint (CRITICAL — missing before this fix)
	if j.joinResp.Endpoint != "" {
		if parsed, err := url.Parse(j.joinResp.Endpoint); err == nil {
			log.Printf("[bypass] WS endpoint host: %s", parsed.Hostname())
			add(parsed.Hostname())
		}
	}

	// 2. TURN servers from the actual session
	for _, u := range j.joinResp.TurnURLs {
		host := extractTurnHost(u)
		add(host)
	}

	// 3. STUN servers
	for _, u := range j.joinResp.StunURLs {
		host := extractTurnHost(u)
		add(host)
	}

	// 4. Remote ICE candidate IPs (VPS public IP for P2P/DIRECT connections)
	for ip := range j.remoteIPs {
		if !seen[ip] {
			seen[ip] = true
			ips = append(ips, ip)
			log.Printf("[bypass] adding remote ICE IP: %s", ip)
		}
	}

	return ips
}

func extractTurnHost(turnURL string) string {
	s := turnURL
	for _, prefix := range []string{"turn:", "turns:", "stun:", "stuns:"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
			break
		}
	}
	if qIdx := strings.Index(s, "?"); qIdx >= 0 {
		s = s[:qIdx]
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return s
	}
	return host
}

func (j *Joiner) Run() error {
	j.joinResp.Endpoint = j.auth.Endpoint
	j.joinResp.TurnURLs = j.auth.TurnURLs
	j.joinResp.TurnUser = j.auth.TurnUser
	j.joinResp.TurnCred = j.auth.TurnCred
	j.joinResp.StunURLs = j.auth.StunURLs
	log.Printf("Joiner: using pre-resolved session params, turn=%v", j.joinResp.TurnURLs)

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

	// Bind ICE to physical adapter ONLY.
	// Without this, ICE uses 0.0.0.0 and routing table changes cause source IP shift → ICE dies.
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4,
		webrtc.NetworkTypeTCP4,
	})

	// Find the physical adapter's IP (non-loopback, non-TUN, IPv4)
	physicalIP := getPhysicalIP()
	if physicalIP != "" {
		log.Printf("[ice] Binding ICE to physical adapter IP: %s", physicalIP)
		se.SetNAT1To1IPs([]string{physicalIP}, webrtc.ICECandidateTypeHost)

		// Create a UDP listener bound directly to the physical adapter IP to lock routing
		addr, err := net.ResolveUDPAddr("udp4", physicalIP+":0")
		if err == nil {
			udpListener, err := net.ListenUDP("udp4", addr)
			if err == nil {
				log.Printf("[ice] Shared UDP mux bound successfully to %s", udpListener.LocalAddr().String())
				j.udpListener = udpListener
				se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpListener))
			} else {
				log.Printf("[ice] Failed to listen on physical UDP: %v", err)
			}
		} else {
			log.Printf("[ice] Failed to resolve physical UDP addr: %v", err)
		}

		se.SetIPFilter(func(ip net.IP) bool {
			// Only allow the physical adapter's IP for ICE
			allowed := ip.String() == physicalIP
			if !allowed {
				log.Printf("[ice] filtering out IP: %s", ip.String())
			}
			return allowed
		})
	}

	se.SetInterfaceFilter(func(iface string) bool {
		excluded := []string{"WLVPN", "tun", "Wintun", "TAP", "Loopback", "Hyper-V", "vEthernet"}
		ifaceLower := strings.ToLower(iface)
		for _, ex := range excluded {
			if strings.Contains(ifaceLower, strings.ToLower(ex)) {
				log.Printf("[ice] excluding interface: %s", iface)
				return false
			}
		}
		log.Printf("[ice] using interface: %s", iface)
		return true
	})

	me := &webrtc.MediaEngine{}
	me.RegisterDefaultCodecs()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
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
	// High-frequency data transmission logging removed to prevent terminal I/O latency.
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
		j.mu.Lock()
		ch := j.connectCh[connID]
		j.mu.Unlock()
		if ch != nil {
			ch <- nil
		}
	case msgConnectErr:
		errMsg := string(payload)
		log.Printf("[socks] conn %d: connect error: %s", connID, errMsg)
		j.mu.Lock()
		ch := j.connectCh[connID]
		j.mu.Unlock()
		if ch != nil {
			ch <- fmt.Errorf("%s", errMsg)
		}
	case msgData:
		j.mu.Lock()
		conn := j.socksConns[connID]
		j.mu.Unlock()
		if conn != nil {
			_, err := conn.Write(payload)
			if err != nil {
				log.Printf("[dc-in] conn %d: write error: %v", connID, err)
			}
		} else {
			log.Printf("[dc-in] conn %d: no local conn found!", connID)
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
	defer conn.Close()

	// SOCKS5 handshake
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != 0x05 {
		return
	}
	conn.Write([]byte{0x05, 0x00}) // no auth

	n, err = conn.Read(buf)
	if err != nil || n < 7 {
		return
	}

	cmd := buf[1]
	if cmd == 0x03 {
		// UDP ASSOCIATE — not supported, reply with command not supported
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if cmd != 0x01 {
		// Not TCP CONNECT
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var target string
	switch buf[3] {
	case 0x01: // IPv4
		if n < 10 {
			return
		}
		ip := net.IP(buf[4:8])
		port := binary.BigEndian.Uint16(buf[8:10])
		target = fmt.Sprintf("%s:%d", ip.String(), port)
	case 0x03: // Domain
		domainLen := int(buf[4])
		if n < 5+domainLen+2 {
			return
		}
		domain := string(buf[5 : 5+domainLen])
		port := binary.BigEndian.Uint16(buf[5+domainLen : 5+domainLen+2])
		target = fmt.Sprintf("%s:%d", domain, port)
	case 0x04: // IPv6
		if n < 22 {
			return
		}
		ip := net.IP(buf[4:20])
		port := binary.BigEndian.Uint16(buf[20:22])
		target = fmt.Sprintf("[%s]:%d", ip.String(), port)
	default:
		return
	}

	log.Printf("[socks] new TCP CONNECT -> %s", target)

	// Send CONNECT to creator via DC and WAIT for ConnectOK
	j.connIDMu.Lock()
	j.connIDSeq++
	connID := j.connIDSeq
	j.connIDMu.Unlock()

	waitCh := make(chan error, 1)
	j.mu.Lock()
	j.socksConns[connID] = conn
	j.connectCh[connID] = waitCh
	j.mu.Unlock()

	j.sendDCFrame(connID, msgConnect, []byte(target))

	// Wait for server to dial the target (ConnectOK or ConnectErr)
	select {
	case err := <-waitCh:
		j.mu.Lock()
		delete(j.connectCh, connID)
		j.mu.Unlock()
		if err != nil {
			log.Printf("[socks] conn %d: server connect failed: %v", connID, err)
			conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // connection refused
			j.mu.Lock()
			delete(j.socksConns, connID)
			j.mu.Unlock()
			return
		}
	case <-time.After(30 * time.Second):
		log.Printf("[socks] conn %d: connect timeout", connID)
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // host unreachable
		j.mu.Lock()
		delete(j.socksConns, connID)
		delete(j.connectCh, connID)
		j.mu.Unlock()
		return
	}

	// NOW reply success — server has confirmed the connection
	log.Printf("[socks] conn %d: sending SOCKS5 success", connID)
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Read from local conn -> send to DC
	rbuf := make([]byte, 32768)
	for {
		n, err := conn.Read(rbuf)
		if n > 0 {
			j.sendDCFrame(connID, msgData, rbuf[:n])
		}
		if err != nil {
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
			// Extract IP from candidate for bypass routing
			if ice.Candidate != "" {
				parts := strings.Fields(ice.Candidate)
				if len(parts) >= 5 {
					candIP := parts[4]
					parsedIP := net.ParseIP(candIP)
					if parsedIP != nil {
						// === ELIMINATE ICE FAILURE MODES ===
						// 1. Skip IPv6 candidates completely to avoid routing/connection issues
						if parsedIP.To4() == nil {
							log.Printf("[bypass] Ignoring remote IPv6 candidate: %s", candIP)
							return
						}
						// 2. Skip private IPv4 candidates (like Docker 172.17.0.1) as they are unreachable
						// and can trick the ICE agent into choosing a dead nominated pair
						if isPrivateIP(parsedIP) {
							log.Printf("[bypass] Ignoring unreachable remote private candidate: %s", candIP)
							return
						}

						if !j.remoteIPs[candIP] {
							j.remoteIPs[candIP] = true
							log.Printf("[bypass] remote ICE candidate IP: %s", candIP)
						}
					}
				}
			}
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
			filteredSDP := j.filterAndExtractSDPCandidates(sdpStr)
			j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: filteredSDP})
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
			filteredSDP := j.filterAndExtractSDPCandidates(sdpStr)
			j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: filteredSDP})
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
	if j.udpListener != nil {
		j.udpListener.Close()
		log.Printf("[ice] Shared UDP mux closed.")
	}
	StopCaptchaProxy()
}

// getPhysicalIP discovers the IP of the default physical adapter by doing a
// UDP "connect" to an external IP (no actual data is sent).
func getPhysicalIP() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		log.Printf("[ice] getPhysicalIP failed: %v", err)
		return ""
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// isPrivateIP checks if the IP belongs to private subnetworks (RFC 1918, RFC 4193 etc).
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	privateBlocks := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	for _, block := range privateBlocks {
		_, subnet, _ := net.ParseCIDR(block)
		if subnet != nil && subnet.Contains(ip) {
			return true
		}
	}
	return false
}

// filterAndExtractSDPCandidates parses an SDP string, strips out candidate lines (a=candidate:)
// that belong to IPv6 or private IPv4 subnets, and extracts valid remote public IPv4 candidates
// to the remoteIPs bypass list so they bypass the VPN virtual gateway.
func (j *Joiner) filterAndExtractSDPCandidates(sdp string) string {
	lines := strings.Split(sdp, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "a=candidate:") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 5 {
				ipStr := parts[4]
				ip := net.ParseIP(ipStr)
				if ip != nil {
					if ip.To4() == nil || isPrivateIP(ip) {
						log.Printf("[sdp-filter] Stripping remote inline candidate: %s", ipStr)
						continue // skip this line
					}
					// Add valid remote public IPv4 candidate to remoteIPs bypass list
					if !j.remoteIPs[ipStr] {
						j.remoteIPs[ipStr] = true
						log.Printf("[bypass] Extracted SDP remote ICE candidate IP: %s", ipStr)
					}
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
