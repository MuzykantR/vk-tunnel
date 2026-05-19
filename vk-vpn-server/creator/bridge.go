package creator

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
	vkice "github.com/vk-vpn/server/webrtc"
)

// Bridge — WS + P2P creator (whitelist-bypass headless/vk).
type Bridge struct {
	mu          sync.Mutex
	ws          *websocket.Conn
	vkSeq       int
	iceServers  []pion.ICEServer
	topology    string
	peers       map[int64]struct{}
	session     *TunnelSession
	p2p         *P2PHandler
	wsURL        string
	appVersion   string
	protoVersion string
	sessionOpts  SessionOpts
}

func NewBridge(wsURL string, ice vkice.ICEConfig, appVer, protoVer string, sessOpts SessionOpts) (*Bridge, error) {
	servers := buildICE(ice)
	sess, err := NewTunnelSession(servers, sessOpts)
	if err != nil {
		return nil, err
	}
	b := &Bridge{
		iceServers:   servers,
		topology:     TopologyDirect,
		peers:        make(map[int64]struct{}),
		session:      sess,
		wsURL:        wsURL,
		appVersion:   appVer,
		protoVersion: protoVer,
		sessionOpts:  sessOpts,
	}
	sess.OnCloseReq = func() {
		log.Println("[creator] PeerConnection disconnected/failed. Initiating call recycle...")
		b.Close()
	}
	b.p2p = NewP2PHandler(b)
	b.p2p.setupCallbacks()
	if err := b.p2p.Init(); err != nil {
		sess.Close()
		return nil, err
	}
	return b, nil
}

func buildICE(ice vkice.ICEConfig) []pion.ICEServer {
	var s []pion.ICEServer
	if len(ice.StunURLs) > 0 {
		s = append(s, pion.ICEServer{URLs: ice.StunURLs})
	}
	if len(ice.TurnURLs) > 0 {
		urls := append([]string{}, ice.TurnURLs...)
		urls = append(urls, urls[len(urls)-1]+"?transport=tcp")
		s = append(s, pion.ICEServer{URLs: urls, Username: ice.TurnUser, Credential: ice.TurnCred})
	}
	if len(s) == 0 {
		s = append(s, pion.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}})
	}
	return s
}

func (b *Bridge) Run() {
	go b.pingLoop()
	b.readLoop()
}

func (b *Bridge) Connect() error {
	header := http.Header{}
	header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	header.Set("Origin", "https://vk.com")
	ws, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second, WriteBufferSize: 65536}).Dial(b.wsURL, header)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.ws = ws
	b.vkSeq = 0
	b.mu.Unlock()
	logx.L("vk-ws", "connected")

	b.vkSend("change-media-settings", map[string]interface{}{
		"mediaSettings": map[string]interface{}{
			"isAudioEnabled": false, "isVideoEnabled": true,
			"isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false,
			"isAudioSharingEnabled": false, "isAnimojiEnabled": false,
		},
	})
	return nil
}

func (b *Bridge) Close() {
	b.mu.Lock()
	if b.ws != nil {
		b.ws.Close()
		b.ws = nil
	}
	b.mu.Unlock()
	if b.session != nil {
		b.session.Close()
	}
}

func (b *Bridge) vkSend(command string, extra map[string]interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ws == nil {
		return
	}
	b.vkSeq++
	seq := b.vkSeq
	var out []byte
	if pid, ok := extra["participantId"]; ok {
		dataJSON, _ := json.Marshal(extra["data"])
		out = []byte(fmt.Sprintf(`{"command":%q,"sequence":%d,"participantId":%v,"data":%s}`, command, seq, pid, dataJSON))
	} else {
		extra["command"] = command
		extra["sequence"] = seq
		out, _ = json.Marshal(extra)
	}
	b.ws.WriteMessage(websocket.TextMessage, out)
	log.Printf("[vk-ws] -> %s", command)
}

func (b *Bridge) vkSendTransmit(participantID int64, data map[string]interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ws == nil {
		return
	}
	b.vkSeq++
	dataJSON, _ := json.Marshal(data)
	out := fmt.Sprintf(`{"command":"transmit-data","sequence":%d,"participantId":%d,"data":%s,"participantType":"USER"}`,
		b.vkSeq, participantID, dataJSON)
	b.ws.WriteMessage(websocket.TextMessage, []byte(out))
	log.Printf("[vk-ws] -> transmit-data (peer %d)", participantID)
}

func (b *Bridge) pingLoop() {
	for {
		time.Sleep(15 * time.Second)
		b.mu.Lock()
		ws := b.ws
		b.mu.Unlock()
		if ws == nil {
			return
		}
		b.mu.Lock()
		if b.ws != nil {
			b.ws.WriteMessage(websocket.PingMessage, nil)
		}
		b.mu.Unlock()
	}
}

func (b *Bridge) readLoop() {
	for {
		b.mu.Lock()
		ws := b.ws
		b.mu.Unlock()
		if ws == nil {
			return
		}
		_, msg, err := ws.ReadMessage()
		if err != nil {
			log.Printf("[vk-ws] read error: %v", err)
			return
		}
		if string(msg) == "ping" {
			b.mu.Lock()
			if b.ws != nil {
				b.ws.WriteMessage(websocket.TextMessage, []byte("pong"))
			}
			b.mu.Unlock()
			continue
		}
		b.handleVKMessage(msg)
	}
}

func parsePID(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

func (b *Bridge) handleVKMessage(raw []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	msgType, _ := msg["type"].(string)
	switch msgType {
	case "notification":
		// handled below
	case "response":
		seq, _ := msg["sequence"].(float64)
		logx.Debug("vk-ws", "response seq=%d", int(seq))
		return
	case "error":
		errMsg, _ := msg["message"].(string)
		errCode, _ := msg["error"].(string)
		logx.Error("vk-ws", "error %s %s", errCode, errMsg)
		return
	default:
		logx.Warn("vk-ws", "unknown type %s", msgType)
		return
	}
	notif, _ := msg["notification"].(string)

	switch notif {
	case "connection":
		logx.L("vk-ws", "TURN / connection params")

	case "transmitted-data":
		logx.Debug("vk-ws", "signaling frame")
		data, _ := msg["data"].(map[string]interface{})
		if data != nil && b.topology == TopologyDirect && b.p2p != nil {
			b.p2p.OnTransmittedData(data)
		}

	case "registered-peer":
		if pid, ok := parsePID(msg["participantId"]); ok && b.topology == TopologyDirect && b.p2p != nil {
			b.p2p.OnRegisteredPeer(pid)
		}

	case "topology-changed":
		topo, _ := msg["topology"].(string)
		logx.L("vk-ws", "topology %s", topo)
		b.topology = topo
		if topo != TopologyDirect {
			logx.Warn("vk-ws", "not DIRECT — kicking %d peers", len(b.peers))
			for pid := range b.peers {
				b.vkSend("remove-participant", map[string]interface{}{
					"participantId": pid,
					"ban":           false,
				})
			}
		}

	case "participant-joined", "participant-added":
		if pid, ok := parsePID(msg["participantId"]); ok {
			b.peers[pid] = struct{}{}
			logx.L("vk-ws", "participant %d joined (total %d topo=%s)", pid, len(b.peers), b.topology)
			if b.topology != TopologyDirect {
				logx.Warn("vk-ws", "kicking %d (need DIRECT)", pid)
				b.vkSend("remove-participant", map[string]interface{}{
					"participantId": pid,
					"ban":           false,
				})
			} else if b.p2p != nil {
				b.p2p.OnRegisteredPeer(pid)
			}
		}

	case "participant-left", "hungup":
		if pid, ok := parsePID(msg["participantId"]); ok {
			delete(b.peers, pid)
			logx.L("vk-ws", "participant %d left (total %d)", pid, len(b.peers))
		}

	default:
		if notif != "" {
			logx.L("vk-ws", "notification %s", notif)
		}
	}
}
