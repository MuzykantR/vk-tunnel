package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/parser"
	"github.com/vk-vpn/client/socks"
	"github.com/vk-vpn/client/tunnel"
)

type Client struct {
	pc         *webrtc.PeerConnection
	dc         *webrtc.DataChannel
	ctx        context.Context
	payload    *parser.VPNPayload
	wsConn     *websocket.Conn
	mu         sync.Mutex
	vkSeq      int
	peerID     int64
	join       *JoinResult
	remoteSet  bool
	pendingICE []webrtc.ICECandidateInit
	readyCh    chan struct{}
	readyOnce  sync.Once
	socksPort  int
}

func NewClient(ctx context.Context, payload *parser.VPNPayload, socksPort int) (*Client, error) {
	return &Client{
		ctx:       ctx,
		payload:   payload,
		readyCh:   make(chan struct{}),
		socksPort: socksPort,
	}, nil
}

func (c *Client) Ready() <-chan struct{} {
	return c.readyCh
}

func (c *Client) vkSend(command string, extra map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsConn == nil {
		return
	}
	c.vkSeq++
	seq := c.vkSeq
	var out []byte
	if pid, ok := extra["participantId"]; ok {
		dataJSON, _ := json.Marshal(extra["data"])
		out = []byte(fmt.Sprintf(`{"command":%q,"sequence":%d,"participantId":%v,"data":%s}`,
			command, seq, pid, dataJSON))
	} else {
		extra["command"] = command
		extra["sequence"] = seq
		out, _ = json.Marshal(extra)
	}
	c.wsConn.WriteMessage(websocket.TextMessage, out)
}

func (c *Client) vkSendTransmit(participantID int64, data map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsConn == nil {
		return
	}
	c.vkSeq++
	dataJSON, _ := json.Marshal(data)
	out := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":%s,"participantType":"USER"}`,
		c.vkSeq, participantID, dataJSON)
	c.wsConn.WriteMessage(websocket.TextMessage, []byte(out))
}

func buildICEServers(join *JoinResult) []webrtc.ICEServer {
	var servers []webrtc.ICEServer
	if len(join.StunURLs) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: join.StunURLs})
	}
	if len(join.TurnURLs) > 0 {
		urls := append([]string{}, join.TurnURLs...)
		urls = append(urls, urls[len(urls)-1]+"?transport=tcp")
		servers = append(servers, webrtc.ICEServer{
			URLs:       urls,
			Username:   join.TurnUser,
			Credential: join.TurnCred,
		})
	}
	if len(servers) == 0 {
		servers = append(servers, webrtc.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}})
	}
	return servers
}

func (c *Client) Connect() error {
	log.Println("Setting up WebRTC tracks and DataChannels for VK SFU...")
	log.Println("Resolving join link via VK HTTP API...")

	join, err := ResolveJoinLink(c.payload.Link)
	if err != nil {
		return err
	}
	c.join = join

	settingEngine := webrtc.SettingEngine{}
	settingEngine.DetachDataChannels()

	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: buildICEServers(join)})
	if err != nil {
		return err
	}
	c.pc = pc

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("Client ICE state: %s", state.String())
	})

	audioTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "tunnel-audio",
	)
	if audioTrack != nil {
		pc.AddTrack(audioTrack)
	}
	videoTrack, _ := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "tunnel-video",
	)
	if videoTrack != nil {
		pc.AddTrack(videoTrack)
	}

	ordered := true
	pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	negotiated := true
	dcID := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &dcID,
	})
	if err != nil {
		return err
	}
	c.dc = dc

	dc.OnOpen(func() {
		log.Println("Tunnel DataChannel open — starting SOCKS5 relay")
		dt := tunnel.NewDCTunnel(dc, socks.DCBufSize, log.Printf)
		bridge := tunnel.NewRelayBridge(dt, "joiner", log.Printf)
		bridge.MarkReady()
		c.readyOnce.Do(func() { close(c.readyCh) })
		go func() {
			addr := fmt.Sprintf("127.0.0.1:%d", c.socksPort)
			if err := bridge.ListenSOCKS(addr); err != nil {
				log.Printf("SOCKS5 relay error: %v", err)
			}
		}()
	})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || c.peerID == 0 {
			return
		}
		candJSON, _ := json.Marshal(candidate.ToJSON())
		var parsed interface{}
		json.Unmarshal(candJSON, &parsed)
		c.vkSendTransmit(c.peerID, map[string]interface{}{"candidate": parsed})
	})

	header := http.Header{}
	header.Set("Origin", "https://vk.com")
	header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	wsConn, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).DialContext(c.ctx, join.WsURL, header)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	c.wsConn = wsConn
	log.Println("Connected to VK WebSocket successfully")

	c.vkSend("update-media-modifiers", map[string]interface{}{
		"mediaModifiers": map[string]interface{}{"denoise": true, "denoiseAnn": true},
	})
	c.vkSend("change-media-settings", map[string]interface{}{
		"mediaSettings": map[string]interface{}{
			"isAudioEnabled": false, "isVideoEnabled": true,
			"isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false,
			"isAudioSharingEnabled": false, "isAnimojiEnabled": false,
		},
	})

	go c.readLoop()
	return nil
}

func (c *Client) readLoop() {
	for {
		_, msg, err := c.wsConn.ReadMessage()
		if err != nil {
			log.Printf("VK WS read error: %v", err)
			return
		}
		if string(msg) == "ping" {
			c.mu.Lock()
			c.wsConn.WriteMessage(websocket.TextMessage, []byte("pong"))
			c.mu.Unlock()
			continue
		}
		c.handleVKMessage(msg)
	}
}

func (c *Client) handleVKMessage(raw []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	if msg["type"] != "notification" {
		return
	}
	notif, _ := msg["notification"].(string)
	switch notif {
	case "registered-peer":
		if pid, ok := msg["participantId"].(float64); ok {
			c.peerID = int64(pid)
			log.Printf("Registered peer ID: %d", c.peerID)
		}
	case "transmitted-data":
		if pid, ok := msg["participantId"].(float64); ok && c.peerID == 0 {
			c.peerID = int64(pid)
		}
		data, _ := msg["data"].(map[string]interface{})
		if data == nil {
			return
		}
		if candObj, ok := data["candidate"]; ok {
			candJSON, _ := json.Marshal(candObj)
			var ice webrtc.ICECandidateInit
			if json.Unmarshal(candJSON, &ice) == nil {
				if c.remoteSet {
					c.pc.AddICECandidate(ice)
				} else {
					c.pendingICE = append(c.pendingICE, ice)
				}
			}
		}
		if sdpObj, ok := data["sdp"].(map[string]interface{}); ok {
			sdpType, _ := sdpObj["type"].(string)
			sdpStr, _ := sdpObj["sdp"].(string)
			if sdpType == "offer" {
				c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdpStr})
				c.remoteSet = true
				for _, ice := range c.pendingICE {
					c.pc.AddICECandidate(ice)
				}
				c.pendingICE = nil

				ans, err := c.pc.CreateAnswer(nil)
				if err != nil || c.peerID == 0 {
					log.Printf("CreateAnswer failed: %v", err)
					return
				}
				c.pc.SetLocalDescription(ans)
				sdpJSON, _ := json.Marshal(ans.SDP)
				c.mu.Lock()
				if c.wsConn != nil {
					c.vkSeq++
					raw := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":{"sdp":{"sdp":%s,"type":%q},"animojiVersion":2},"participantType":"USER"}`,
						c.vkSeq, c.peerID, sdpJSON, ans.Type.String())
					c.wsConn.WriteMessage(websocket.TextMessage, []byte(raw))
					log.Println("Sent SDP answer to VK SFU")
				}
				c.mu.Unlock()
			}
		}
	case "connection":
		if params, ok := msg["conversationParams"].(map[string]interface{}); ok {
			if turn, ok := params["turn"].(map[string]interface{}); ok {
				c.updateTURN(turn)
			}
		}
	}
}

func (c *Client) updateTURN(turn map[string]interface{}) {
	urlsRaw, _ := turn["urls"].([]interface{})
	var urls []string
	for _, u := range urlsRaw {
		if s, ok := u.(string); ok {
			urls = append(urls, s)
		}
	}
	user, _ := turn["username"].(string)
	cred, _ := turn["credential"].(string)
	if len(urls) == 0 {
		return
	}
	urls = append(urls, urls[len(urls)-1]+"?transport=tcp")
	_ = c.pc.SetConfiguration(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: urls, Username: user, Credential: cred}},
	})
	log.Printf("Updated TURN servers: %v", urls)
}

func (c *Client) Close() {
	if c.pc != nil {
		c.pc.Close()
	}
	if c.wsConn != nil {
		c.wsConn.Close()
	}
	StopCaptchaProxy()
}

// OpenCaptchaBrowser opens the local captcha proxy in the default browser.
func OpenCaptchaBrowser(port int) {
	exec.Command("cmd", "/c", "start", fmt.Sprintf("http://127.0.0.1:%d/", port)).Run()
}
