package webrtc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/socks"
	"github.com/vk-vpn/client/tunnel"
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
}

func NewJoiner(ctx context.Context, auth *AuthParams, socksPort int) *Joiner {
	return &Joiner{
		auth:      auth,
		socksPort: socksPort,
		readyCh:   make(chan struct{}),
		ctx:       ctx,
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
	se := webrtc.SettingEngine{}
	se.DetachDataChannels()
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		log.Printf("Joiner: PC failed: %v", err)
		return
	}
	j.pc = pc

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Printf("Client ICE state: %s", s.String())
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
		log.Println("Tunnel DataChannel open — starting SOCKS5 relay")
		dt := tunnel.NewDCTunnel(dc, socks.DCBufSize, log.Printf)
		bridge := tunnel.NewRelayBridge(dt, "joiner", log.Printf)
		bridge.MarkReady()
		j.readyOnce.Do(func() { close(j.readyCh) })
		go bridge.ListenSOCKS(fmt.Sprintf("127.0.0.1:%d", j.socksPort))
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || j.remotePeerID == nil {
			return
		}
		j.vkSendTransmit(*j.remotePeerID, map[string]interface{}{"candidate": c.ToJSON()})
	})
	log.Println("Joiner: PC ready, waiting for offer")
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
		log.Printf("Remote SDP: %s", sdpType)
		if sdpType == "answer" {
			j.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdpStr})
			j.remoteSet = true
			for _, ice := range j.pendingICE {
				j.pc.AddICECandidate(ice)
			}
			j.pendingICE = nil
		} else if sdpType == "offer" {
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
