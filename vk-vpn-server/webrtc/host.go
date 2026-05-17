package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"
	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/socks"
	"github.com/vk-vpn/client/tunnel"
)

// ICEConfig from joinConversationByLink.
type ICEConfig struct {
	StunURLs []string
	TurnURLs []string
	TurnUser string
	TurnCred string
}

// Host runs creator-side WebRTC on the VK SFU and relays traffic to the internet.
type Host struct {
	ws        *websocket.Conn
	mu        sync.Mutex
	vkSeq     int
	peerID    int64
	pc        *pion.PeerConnection
	dc        *pion.DataChannel
	ice       ICEConfig
	remoteSet bool
	pending   []pion.ICECandidateInit
}

func NewHost(ws *websocket.Conn, ice ICEConfig) *Host {
	return &Host{ws: ws, ice: ice}
}

func (h *Host) Start() error {
	settingEngine := pion.SettingEngine{}
	settingEngine.DetachDataChannels()

	api := pion.NewAPI(pion.WithSettingEngine(settingEngine))
	pc, err := api.NewPeerConnection(pion.Configuration{ICEServers: h.buildICE()})
	if err != nil {
		return err
	}
	h.pc = pc

	pc.OnICEConnectionStateChange(func(s pion.ICEConnectionState) {
		log.Printf("Server ICE state: %s", s.String())
	})

	audio, _ := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus}, "audio", "a")
	if audio != nil {
		pc.AddTrack(audio)
	}
	video, _ := pion.NewTrackLocalStaticSample(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8}, "video", "v")
	if video != nil {
		pc.AddTrack(video)
	}

	ordered := true
	pc.CreateDataChannel("producerNotification", &pion.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerCommand", &pion.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("producerScreenShare", &pion.DataChannelInit{Ordered: &ordered})
	pc.CreateDataChannel("consumerScreenShare", &pion.DataChannelInit{Ordered: &ordered})

	neg := true
	id := uint16(2)
	dc, err := pc.CreateDataChannel("tunnel", &pion.DataChannelInit{Negotiated: &neg, ID: &id})
	if err != nil {
		return err
	}
	h.dc = dc

	dc.OnOpen(func() {
		log.Println("Server tunnel DataChannel open — starting creator relay")
		dt := tunnel.NewDCTunnel(dc, socks.DCBufSize, log.Printf)
		bridge := tunnel.NewRelayBridge(dt, "creator", log.Printf)
		bridge.MarkReady()
	})

	pc.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil || h.peerID == 0 {
			return
		}
		candJSON, _ := json.Marshal(c.ToJSON())
		var parsed interface{}
		json.Unmarshal(candJSON, &parsed)
		h.sendTransmit(map[string]interface{}{"candidate": parsed})
	})

	h.vkSend("update-media-modifiers", map[string]interface{}{
		"mediaModifiers": map[string]interface{}{"denoise": true, "denoiseAnn": true},
	})
	h.vkSend("change-media-settings", map[string]interface{}{
		"mediaSettings": map[string]interface{}{
			"isAudioEnabled": false, "isVideoEnabled": true,
			"isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false,
			"isAudioSharingEnabled": false, "isAnimojiEnabled": false,
		},
	})
	return nil
}

func (h *Host) buildICE() []pion.ICEServer {
	var s []pion.ICEServer
	if len(h.ice.StunURLs) > 0 {
		s = append(s, pion.ICEServer{URLs: h.ice.StunURLs})
	}
	if len(h.ice.TurnURLs) > 0 {
		urls := append([]string{}, h.ice.TurnURLs...)
		urls = append(urls, urls[len(urls)-1]+"?transport=tcp")
		s = append(s, pion.ICEServer{URLs: urls, Username: h.ice.TurnUser, Credential: h.ice.TurnCred})
	}
	if len(s) == 0 {
		s = append(s, pion.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}})
	}
	return s
}

func (h *Host) vkSend(command string, extra map[string]interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ws == nil {
		return
	}
	h.vkSeq++
	seq := h.vkSeq
	var out []byte
	if pid, ok := extra["participantId"]; ok {
		dataJSON, _ := json.Marshal(extra["data"])
		out = []byte(fmt.Sprintf(`{"command":%q,"sequence":%d,"participantId":%v,"data":%s}`, command, seq, pid, dataJSON))
	} else {
		extra["command"] = command
		extra["sequence"] = seq
		out, _ = json.Marshal(extra)
	}
	h.ws.WriteMessage(websocket.TextMessage, out)
}

func (h *Host) sendTransmit(data map[string]interface{}) {
	if h.peerID == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ws == nil {
		return
	}
	h.vkSeq++
	dataJSON, _ := json.Marshal(data)
	out := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":%s,"participantType":"USER"}`,
		h.vkSeq, h.peerID, dataJSON)
	h.ws.WriteMessage(websocket.TextMessage, []byte(out))
}

func (h *Host) HandleNotification(msg map[string]interface{}) {
	notif, _ := msg["notification"].(string)
	switch notif {
	case "registered-peer":
		if pid, ok := msg["participantId"].(float64); ok {
			h.peerID = int64(pid)
			log.Printf("Server: remote peer %d", h.peerID)
		}
	case "transmitted-data":
		if pid, ok := msg["participantId"].(float64); ok && h.peerID == 0 {
			h.peerID = int64(pid)
		}
		data, _ := msg["data"].(map[string]interface{})
		if data == nil || h.pc == nil {
			return
		}
		if cand, ok := data["candidate"]; ok {
			b, _ := json.Marshal(cand)
			var ice pion.ICECandidateInit
			if json.Unmarshal(b, &ice) == nil {
				if h.remoteSet {
					h.pc.AddICECandidate(ice)
				} else {
					h.pending = append(h.pending, ice)
				}
			}
		}
		if sdp, ok := data["sdp"].(map[string]interface{}); ok {
			sdpType, _ := sdp["type"].(string)
			sdpStr, _ := sdp["sdp"].(string)
			if sdpType == "offer" {
				h.pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: sdpStr})
				h.remoteSet = true
				for _, ice := range h.pending {
					h.pc.AddICECandidate(ice)
				}
				h.pending = nil
				ans, err := h.pc.CreateAnswer(nil)
				if err != nil || h.peerID == 0 {
					return
				}
				h.pc.SetLocalDescription(ans)
				sdpJSON, _ := json.Marshal(ans.SDP)
				h.mu.Lock()
				if h.ws != nil {
					h.vkSeq++
					raw := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":{"sdp":{"sdp":%s,"type":%q},"animojiVersion":2},"participantType":"USER"}`,
						h.vkSeq, h.peerID, sdpJSON, ans.Type.String())
					h.ws.WriteMessage(websocket.TextMessage, []byte(raw))
					log.Println("Server: sent SDP answer")
				}
				h.mu.Unlock()
			}
		}
	case "connection":
		if params, ok := msg["conversationParams"].(map[string]interface{}); ok {
			if turn, ok := params["turn"].(map[string]interface{}); ok {
				h.updateTURN(turn)
			}
		}
	}
}

func (h *Host) updateTURN(turn map[string]interface{}) {
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
	_ = h.pc.SetConfiguration(pion.Configuration{
		ICEServers: []pion.ICEServer{{URLs: urls, Username: user, Credential: cred}},
	})
}

func (h *Host) Close() {
	if h.pc != nil {
		h.pc.Close()
	}
}

// ParseICEFromJoin extracts STUN/TURN from joinConversation JSON body.
func ParseICEFromJoin(body []byte) ICEConfig {
	var cfg ICEConfig
	var r map[string]interface{}
	if json.Unmarshal(body, &r) != nil {
		return cfg
	}
	if turn, ok := r["turn_server"].(map[string]interface{}); ok {
		cfg.TurnUser, _ = turn["username"].(string)
		cfg.TurnCred, _ = turn["credential"].(string)
		if urls, ok := turn["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					cfg.TurnURLs = append(cfg.TurnURLs, s)
				}
			}
		}
	}
	if stun, ok := r["stun_server"].(map[string]interface{}); ok {
		if urls, ok := stun["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					cfg.StunURLs = append(cfg.StunURLs, s)
				}
			}
		}
	}
	return cfg
}
