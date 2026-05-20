package webrtc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
	"github.com/vk-vpn/client/tunnel"
)

// Joiner — headless VK joiner (whitelist-bypass vk_joiner.go), DC tunnel mode.
type Joiner struct {
	auth      *AuthParams
	socksPort int
	readyCh   chan struct{}
	readyOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc

	joinResp struct {
		Endpoint string
		TurnURLs []string
		TurnUser string
		TurnCred string
		StunURLs []string
	}

	ws            *websocket.Conn
	vkMu          sync.Mutex
	vkSeq         int
	remotePeerID  *int64
	pc            *webrtc.PeerConnection
	dc            *webrtc.DataChannel
	dcTunnel      *tunnel.DCTunnel
	vp8Tunnel     *tunnel.VP8DataTunnel
	sampleTrack   *webrtc.TrackLocalStaticSample
	tunnelMode    string
	relay         *tunnel.RelayBridge
	tunnelWD      *tunnel.TunnelWatchdog
	remoteSet     bool
	firstConnected time.Time
	pendingICE    []webrtc.ICECandidateInit
	answerSent    bool
	remoteIPs     map[string]bool // IPs from remote ICE candidates (VPS IP for P2P bypass)
	mu            sync.Mutex
	closeOnce     sync.Once
	iceStableReached atomic.Bool
	iceStableWaitGen atomic.Uint32
	defaultRouteReady atomic.Bool
	defaultRouteAt    time.Time
	onNewBypassIP func(string) // called the moment a new remote ICE IP is observed
	iceRestartMu  sync.Mutex
	iceRestarting bool

	// iceStableCh fires once when ICE first reaches connected/completed AND has
	// produced at least one successful host-to-host roundtrip (we sample after
	// 1 s of being "connected"). The caller uses this to gate the default-route
	// install — installing 0.0.0.0/1 → WLVPN while ICE is still in "checking"
	// frequently kills the keepalive flow.
	iceStableCh   chan struct{}
	iceStableOnce sync.Once
	obf           *tunnel.TunnelObfuscator
}

func NewJoiner(ctx context.Context, auth *AuthParams, socksPort int) *Joiner {
	childCtx, cancel := context.WithCancel(ctx)
	mode := os.Getenv("VK_VPN_TUNNEL_MODE")
	if mode != "dc" && mode != "video" {
		mode = "video"
	}
	j := &Joiner{
		auth:         auth,
		socksPort:    socksPort,
		readyCh:      make(chan struct{}),
		iceStableCh:  make(chan struct{}),
		ctx:          childCtx,
		cancel:       cancel,
		remoteIPs:    make(map[string]bool),
		tunnelMode:   mode,
	}
	if auth != nil && auth.JoinLink != "" {
		if obf, err := tunnel.NewTunnelObfuscator(tunnel.DeriveSecretFromJoinLink(auth.JoinLink)); err == nil {
			j.obf = obf
			log.Printf("Joiner: obfuscator on (epoch=0x%08x)", obf.LocalEpoch())
		} else {
			log.Printf("Joiner: obfuscator off: %v", err)
		}
	}
	return j
}

func (j *Joiner) Ready() <-chan struct{} { return j.readyCh }

// IceStable fires (closed) once the ICE agent has been in connected/completed
// state continuously for at least 1 second, indicating that the keepalive
// flow is established on the physical adapter and routing-table changes are
// safe to apply.
func (j *Joiner) IceStable() <-chan struct{} { return j.iceStableCh }

// IsICEConnected reports whether the peer connection ICE agent is connected or completed.
func (j *Joiner) IsICEConnected() bool {
	if j.pc == nil {
		return false
	}
	st := j.pc.ICEConnectionState()
	return st == webrtc.ICEConnectionStateConnected || st == webrtc.ICEConnectionStateCompleted
}

// SetDefaultRouteReady marks that split default routes are installed; the tunnel
// watchdog and ICE-restart suppression treat post-redirect recovery differently.
func (j *Joiner) SetDefaultRouteReady() {
	j.iceStableReached.Store(true)
	j.defaultRouteReady.Store(true)
	j.mu.Lock()
	j.defaultRouteAt = time.Now()
	j.mu.Unlock()
}

// ICEBypassIPs returns all public ICE-related IPs that must bypass the TUN.
func (j *Joiner) ICEBypassIPs() []string {
	if j.pc == nil {
		return nil
	}
	return ICEBypassIPs(j.pc)
}

// SetOnNewBypassIP registers a callback invoked the moment a new remote ICE
// candidate IP becomes known. The caller is expected to install a /32 bypass
// route synchronously so the IP never traverses the tunnel. Safe to call once
// before Run().
func (j *Joiner) SetOnNewBypassIP(fn func(string)) {
	j.onNewBypassIP = fn
}

// reportBypassIP fires onNewBypassIP exactly once per unique IPv4 string.
// Called from inside ICE callbacks; non-blocking is the caller's responsibility.
func (j *Joiner) reportBypassIP(ip string) {
	if j.onNewBypassIP != nil {
		j.onNewBypassIP(ip)
	}
}

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

const dcBufferedLowThreshold = 8 * 1024 * 1024 // whitelist-bypass unlimited max-dc-buf

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

	// Minimal SettingEngine, matching whitelist-bypass's approach: trust the
	// routing table to steer ICE through the physical adapter (we install
	// /32 bypass routes for every remote candidate IP synchronously, before
	// pion ever sends a packet). The previous SetNAT1To1IPs / SetIPFilter /
	// SetICEUDPMux / SetInterfaceFilter combo was a brittle workaround that
	// died as soon as Windows touched the routing table during a route ADD.
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4,
		webrtc.NetworkTypeTCP4,
	})
	// Detach DataChannels so the relay reads from a raw ReadWriteCloser in
	// its own goroutine. Without this, pion drives our OnMessage callback
	// from its internal SCTP reader and any slowness in the callback chain
	// (e.g. backpressure from the local SOCKS socket) throttles the whole
	// DataChannel — which is exactly what was happening in our high-RTT,
	// high-parallel-conn tests (read OK pacgs of 20 connect-OKs at a time).
	se.DetachDataChannels()
	// Bigger SCTP receive buffer so high-BDP links (RTT ~109 ms × 50 Mbps
	// = ~700 KB BDP) do not throttle on the default ~1 MB cap.
	se.SetSCTPMaxReceiveBufferSize(8 * 1024 * 1024)
	// Skip per-packet SCTP checksum — pion supports this opt-in for ~10%
	// CPU / latency win and DTLS already authenticates every frame.
	se.EnableSCTPZeroChecksum(true)
	ApplyICEPerformanceSettings(&se)

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
		switch s {
		case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
			j.mu.Lock()
			if j.firstConnected.IsZero() {
				j.firstConnected = time.Now()
				logx.L("joiner", "first ICE connected")
			}
			j.mu.Unlock()
			ScheduleICEPairLogging(j.pc, "joiner")
			go j.maybeMarkICEStable()
		case webrtc.ICEConnectionStateDisconnected:
			// Transient disconnect during first ICE handshake is normal (connected→
			// disconnected before routes settle). WLB does not ICE-restart here; we only
			// restart after the tunnel was stable at least once.
			if j.iceStableReached.Load() {
				go j.handleICEDisconnect()
			} else {
				log.Println("ICE disconnected during handshake — waiting for recovery (no restart)")
			}
		case webrtc.ICEConnectionStateFailed:
			if j.iceStableReached.Load() {
				go j.handleICEFailed()
			} else {
				log.Println("ICE failed during handshake — waiting for recovery (no restart)")
			}
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("Client connection state: %s (tunnel=%s)", s.String(), j.tunnelMode)
		if j.tunnelMode == "video" && s == webrtc.PeerConnectionStateConnected {
			go j.startVideoTunnel()
		}
	})
	if j.tunnelMode == "video" {
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			log.Printf("Joiner: remote track %s", track.Codec().MimeType)
			go tunnel.ReadVP8TrackLogged(track, func(frame []byte) {
				if j.vp8Tunnel != nil {
					j.vp8Tunnel.HandleFrame(frame)
				}
			})
		})
	}

	audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "a")
	if audio != nil {
		pc.AddTrack(audio)
	}
	video, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "v")
	if video != nil {
		j.sampleTrack = video
		pc.AddTrack(video)
	}
	ordered := true
	pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	// Tunnel DataChannel (Negotiated ID: 2). DEFAULTS = ordered + reliable.
	// We rely on Detach() to keep the SCTP reader unblocked under load
	// — the read loop pulls bytes off the wire into Go-side buffers, so a
	// slow consumer downstream never freezes the SCTP transport. Same
	// approach whitelist-bypass uses; it sustains 15 Mbps with this exact
	// config.
	neg := true
	id := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &neg,
		ID:         &id,
	})
	if err != nil {
		return
	}
	j.dc = dc
	dc.OnOpen(func() {
		log.Printf("VPN tunnel DataChannel open (mode=%s)", j.tunnelMode)
		dc.SetBufferedAmountLowThreshold(dcBufferedLowThreshold)
		if j.tunnelMode != "dc" {
			return
		}
		raw, err := dc.Detach()
		if err != nil {
			log.Printf("Joiner: dc.Detach failed: %v", err)
			j.dcTunnel = tunnel.NewDCTunnel(dc, j.obf, 0, log.Printf)
		} else {
			j.dcTunnel = tunnel.NewDCTunnelFromRaw(dc, raw, j.obf, 0, log.Printf)
		}
		j.attachRelay(j.dcTunnel)
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || j.remotePeerID == nil {
			return
		}
		j.vkSendTransmit(*j.remotePeerID, map[string]interface{}{"candidate": c.ToJSON()})
	})
	log.Printf("Joiner: PC ready (tunnel=%s), waiting for offer", j.tunnelMode)
}

func (j *Joiner) attachRelay(t tunnel.DataTunnel) {
	j.mu.Lock()
	if j.relay != nil {
		j.mu.Unlock()
		return
	}
	j.mu.Unlock()
	j.relay = tunnel.NewRelayBridge(t, "joiner", 0, logx.TagPrintf("relay"))
	wd := tunnel.NewTunnelWatchdog(func() {
		j.relay.SendControl(tunnel.MsgPing, nil)
	}, tunnel.WatchdogOpts{
		Interval: 10 * time.Second,
		MaxMiss:  3,
		OnUnhealthy: func() {
			if !j.defaultRouteReady.Load() {
				return
			}
			if j.vp8Tunnel != nil && j.vp8Tunnel.SendQueueDepth() > 0 {
				logx.Warn("tunnel", "watchdog: missed pongs but VP8 send queue busy — ICE restart suppressed")
				return
			}
			logx.Warn("tunnel", "watchdog unhealthy — ICE restart")
			go j.requestICERestart()
		},
	})
	j.relay.SetOnPong(wd.NotifyPong)
	j.tunnelWD = wd
	wd.Start()
	j.relay.MarkReady()
	j.readyOnce.Do(func() { close(j.readyCh) })
	addr := fmt.Sprintf("127.0.0.1:%d", j.socksPort)
	go func() {
		log.Printf("SOCKS5 via RelayBridge (%s) on %s", j.tunnelMode, addr)
		if err := j.relay.ListenSOCKS(addr); err != nil {
			log.Printf("RelayBridge SOCKS stopped: %v", err)
		}
	}()
}

func (j *Joiner) startVideoTunnel() {
	j.mu.Lock()
	if j.vp8Tunnel != nil {
		j.mu.Unlock()
		return
	}
	j.mu.Unlock()
	if j.sampleTrack == nil {
		log.Println("Joiner: video mode but no sample track")
		return
	}
	log.Println("Joiner: === VP8 TUNNEL CONNECTED ===")
	fps, batch := VP8PacingFromEnv()
	j.vp8Tunnel = tunnel.NewVP8DataTunnel(j.sampleTrack, j.obf, logx.TagPrintf("vp8"))
	j.vp8Tunnel.Start(fps, batch)
	j.vp8Tunnel.SendData(tunnel.EncodeVP8Config(j.vp8Tunnel.FPS(), j.vp8Tunnel.Batch()))
	logx.L("joiner", "VP8 pacing fps=%d batch=%d (~%d kbps target)", fps, batch, fps*batch*1126*8/1000)
	j.attachRelay(j.vp8Tunnel)
}

// SOCKS/DC/VP8 relay is handled by tunnel.RelayBridge (whitelist-bypass).

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
							// Synchronously install a /32 bypass BEFORE the candidate is
							// handed to pion. This guarantees STUN keepalives leave through
							// the physical adapter from packet #1 — no race with route ADD.
							j.reportBypassIP(candIP)
							j.reportBypassIP(ice.Candidate)
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
		state := j.pc.SignalingState().String()
		log.Printf("Remote SDP: %s (signalingState=%s, answerSent=%v)", sdpType, state, j.answerSent)
		if sdpType == "answer" {
			filteredSDP := j.filterAndExtractSDPCandidates(sdpStr)
			if err := j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: filteredSDP}); err != nil {
				log.Printf("SetRemoteDescription(answer) failed: %v", err)
				return
			}
			j.remoteSet = true
			for _, ice := range j.pendingICE {
				j.pc.AddICECandidate(ice)
			}
			j.pendingICE = nil
		} else if sdpType == "offer" {
			sigState := j.pc.SignalingState()
			if sigState == webrtc.SignalingStateHaveLocalOffer {
				log.Println("Remote offer while local offer pending — accepting remote (ICE restart / rejoin)")
			} else if j.answerSent && sigState != webrtc.SignalingStateStable {
				log.Println("Ignoring duplicate offer (answer already sent, not in stable)")
				return
			}
			if j.answerSent && sigState == webrtc.SignalingStateStable {
				log.Println("Remote-initiated ICE restart detected, accepting new offer")
			}
			filteredSDP := j.filterAndExtractSDPCandidates(sdpStr)
			if err := j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: filteredSDP}); err != nil {
				log.Printf("SetRemoteDescription(offer) failed: %v", err)
				return
			}
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
				seen := make(map[string]bool, len(j.joinResp.TurnURLs))
				for _, u := range j.joinResp.TurnURLs {
					seen[u] = true
				}
				for _, u := range urlsRaw {
					if s, ok := u.(string); ok && !seen[s] {
						seen[s] = true
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

// Close tears down the joiner exactly once — safe to call from multiple
// goroutines and from inside ICE callbacks.
func (j *Joiner) Close() {
	j.closeOnce.Do(func() {
		log.Println("Joiner: closing...")

		if j.cancel != nil {
			j.cancel()
		}

		if j.vp8Tunnel != nil {
			j.vp8Tunnel.Stop()
		}
		if j.tunnelWD != nil {
			j.tunnelWD.Stop()
		}
		if j.relay != nil {
			j.relay.Close()
		}
		if j.dc != nil {
			j.dc.Close()
		}
		if j.pc != nil {
			j.pc.Close()
		}
		if j.ws != nil {
			j.ws.Close()
		}
		StopCaptchaProxy()
		log.Println("Joiner: closed.")
	})
}

// maybeMarkICEStable closes iceStableCh after ICE stays connected/completed
// for 5s. A newer connected event bumps iceStableWaitGen so stale timers exit.
func (j *Joiner) maybeMarkICEStable() {
	gen := j.iceStableWaitGen.Add(1)
	const stableDur = 5 * time.Second
	timer := time.NewTimer(stableDur)
	defer timer.Stop()
	select {
	case <-j.ctx.Done():
		return
	case <-timer.C:
	}
	if j.iceStableWaitGen.Load() != gen {
		return
	}
	if j.pc == nil {
		return
	}
	st := j.pc.ICEConnectionState()
	if st != webrtc.ICEConnectionStateConnected && st != webrtc.ICEConnectionStateCompleted {
		return
	}
	j.iceStableOnce.Do(func() {
		j.iceStableReached.Store(true)
		log.Printf("ICE held connected/completed for %s (informational; redirect may already be active)", stableDur)
		close(j.iceStableCh)
	})
}

// handleICEDisconnect waits a short grace period for pion's own recovery (it
// may flip back to connected on its own once STUN keepalives resume). If we
// are still in disconnected after the grace, ask the creator side for an
// ICE restart by issuing a renegotiation offer.
func (j *Joiner) handleICEDisconnect() {
	const grace = 12 * time.Second
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-j.ctx.Done():
		return
	case <-timer.C:
	}
	if j.pc == nil {
		return
	}
	state := j.pc.ICEConnectionState()
	if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
		return // recovered on its own
	}
	log.Printf("ICE still %s after %s — requesting ICE restart", state.String(), grace)
	j.requestICERestart()
}

// handleICEFailed is the harder path: pion gave up. Try one ICE restart;
// if that doesn't help, the daemon will eventually rotate the call.
func (j *Joiner) handleICEFailed() {
	log.Println("ICE state: failed — requesting ICE restart immediately")
	j.requestICERestart()
}

// requestICERestart drives the joiner-side of an ICE restart. Because the
// original SDP was offered by the creator (server) and answered by us, the
// cleanest renegotiation is for us to issue a new offer with ICE-Restart=true.
// pion's PeerConnection supports the role flip via SetLocalDescription(offer)
// when SignalingState is stable.
func (j *Joiner) requestICERestart() {
	j.mu.Lock()
	first := j.firstConnected
	j.mu.Unlock()
	if first.IsZero() {
		log.Println("ICE restart: suppressed (never connected)")
		return
	}
	allowEarly := false
	if j.defaultRouteReady.Load() {
		j.mu.Lock()
		sinceRoute := time.Since(j.defaultRouteAt)
		j.mu.Unlock()
		if sinceRoute < 2*time.Minute {
			allowEarly = true
		}
	}
	if !allowEarly && time.Since(first) < 45*time.Second {
		log.Printf("ICE restart: suppressed (within 45s of first connect, elapsed=%s)", time.Since(first).Round(time.Second))
		return
	}

	j.iceRestartMu.Lock()
	if j.iceRestarting {
		j.iceRestartMu.Unlock()
		return
	}
	j.iceRestarting = true
	j.iceRestartMu.Unlock()
	defer func() {
		j.iceRestartMu.Lock()
		j.iceRestarting = false
		j.iceRestartMu.Unlock()
	}()

	if j.pc == nil || j.remotePeerID == nil {
		log.Println("ICE restart: no peer or PC, skipping")
		return
	}
	sig := j.pc.SignalingState()
	if sig != webrtc.SignalingStateStable && sig != webrtc.SignalingStateHaveLocalOffer {
		log.Printf("ICE restart: signaling state %s, skipping", sig.String())
		return
	}
	if sig == webrtc.SignalingStateHaveLocalOffer {
		log.Println("ICE restart: waiting for remote answer or offer (have-local-offer)")
		return
	}
	offer, err := j.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		log.Printf("ICE restart: CreateOffer failed: %v", err)
		return
	}
	if err := j.pc.SetLocalDescription(offer); err != nil {
		log.Printf("ICE restart: SetLocalDescription failed: %v", err)
		return
	}
	j.vkSendTransmit(*j.remotePeerID, map[string]interface{}{
		"sdp": map[string]interface{}{"type": "offer", "sdp": offer.SDP},
	})
	log.Println("ICE restart: sent renegotiation offer to creator")
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
						j.reportBypassIP(ipStr)
						j.reportBypassIP(trimmed)
					}
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
