package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vk-vpn/server/config"
)

type VKSignaling struct {
	wsConn    *websocket.Conn
	cookies   []config.CookieInfo
	mu        sync.Mutex
	EventChan chan map[string]interface{}
	vkSeq     int
	onNotif   func(map[string]interface{})
}

func NewVKSignaling(cookies []config.CookieInfo) *VKSignaling {
	return &VKSignaling{
		cookies:   cookies,
		EventChan: make(chan map[string]interface{}, 100),
	}
}

func (s *VKSignaling) WSConn() *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wsConn
}

func (s *VKSignaling) SetNotificationHandler(fn func(map[string]interface{})) {
	s.mu.Lock()
	s.onNotif = fn
	s.mu.Unlock()
}

// httpPost helper to execute API requests with correct VK headers and cookies
func (s *VKSignaling) httpPost(endpoint string, form url.Values, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Origin", "https://vk.com")
	req.Header.Set("Referer", "https://vk.com/")

	var cookieString string
	for _, c := range s.cookies {
		cookieString += fmt.Sprintf("%s=%s; ", c.Name, c.Value)
	}
	req.Header.Set("Cookie", cookieString)

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// FetchCallURL implements the real logic from whitelist-bypass to create a WebRTC call
// Uses the modern API flow (calls.start -> getCallToken -> anonymLogin -> joinConversation)
func (s *VKSignaling) FetchCallURL(ctx context.Context, peerID string) (wsURL, vkJoinLink, okJoinLink string, iceJSON []byte, err error) {
	log.Printf("Starting VK call via API for peer: %s", peerID)

	appID := "6287487"
	apiVersion := "5.276"

	// 1. Get token
	r, err := s.httpPost("https://login.vk.com/?act=web_token", url.Values{"version": {"1"}, "app_id": {appID}}, nil)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("web_token: %v", err)
	}
	var tok struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	json.Unmarshal(r, &tok)
	vkToken := tok.Data.AccessToken
	if vkToken == "" {
		return "", "", "", nil, fmt.Errorf("empty VK token: %s", string(r))
	}
	auth := map[string]string{"Authorization": "Bearer " + vkToken}

	// 2. Start call
	r, err = s.httpPost("https://api.vk.com/method/calls.start", url.Values{"v": {apiVersion}, "peer_id": {peerID}}, auth)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("calls.start: %v", err)
	}
	var call struct {
		Response struct {
			CallID     string `json:"call_id"`
			JoinLink   string `json:"join_link"`
			OKJoinLink string `json:"ok_join_link"`
		} `json:"response"`
	}
	json.Unmarshal(r, &call)
	okJoinLink = call.Response.OKJoinLink
	vkJoinLink = call.Response.JoinLink
	if okJoinLink == "" {
		return "", "", "", nil, fmt.Errorf("empty ok_join_link: %s", string(r))
	}
	log.Printf("Call created, OK Join Link: %s, VK Join Link: %s", okJoinLink, vkJoinLink)

	// 3. Get settings (Public Key)
	r, err = s.httpPost("https://api.vk.com/method/calls.getSettings", url.Values{"v": {apiVersion}}, auth)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("calls.getSettings: %v", err)
	}
	var settings struct {
		Response struct {
			Settings struct {
				PublicKey string `json:"public_key"`
			} `json:"settings"`
		} `json:"response"`
	}
	json.Unmarshal(r, &settings)
	appKey := settings.Response.Settings.PublicKey

	// 4. Get call token
	r, err = s.httpPost("https://api.vk.com/method/messages.getCallToken", url.Values{"v": {apiVersion}, "env": {"production"}}, auth)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("messages.getCallToken: %v", err)
	}
	var callToken struct {
		Response struct {
			Token      string `json:"token"`
			APIBaseURL string `json:"api_base_url"`
		} `json:"response"`
	}
	json.Unmarshal(r, &callToken)
	apiBaseURL := strings.TrimRight(callToken.Response.APIBaseURL, "/")
	if !strings.HasSuffix(apiBaseURL, "/fb.do") {
		apiBaseURL += "/fb.do"
	}

	// 5. OK Auth (anonymLogin)
	sd, _ := json.Marshal(map[string]interface{}{
		"device_id": "headless-vpn-1", "client_version": "1.1",
		"client_type": "SDK_JS", "auth_token": callToken.Response.Token, "version": 3,
	})
	r, err = s.httpPost(apiBaseURL, url.Values{
		"method": {"auth.anonymLogin"}, "application_key": {appKey},
		"format": {"json"}, "session_data": {string(sd)},
	}, nil)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("auth.anonymLogin: %v", err)
	}
	var okAuth struct {
		SessionKey string `json:"session_key"`
	}
	json.Unmarshal(r, &okAuth)

	// 6. Join conversation
	ms, _ := json.Marshal(map[string]bool{
		"isAudioEnabled": false, "isVideoEnabled": true, "isScreenSharingEnabled": false,
	})
	r, err = s.httpPost(apiBaseURL, url.Values{
		"method": {"vchat.joinConversationByLink"}, "session_key": {okAuth.SessionKey},
		"application_key": {appKey}, "format": {"json"}, "joinLink": {okJoinLink},
		"isVideo": {"true"}, "isAudio": {"false"}, "mediaSettings": {string(ms)},
	}, nil)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("joinConversation: %v", err)
	}
	var joinResp struct {
		Endpoint string `json:"endpoint"`
	}
	json.Unmarshal(r, &joinResp)
	if joinResp.Endpoint == "" {
		return "", "", "", nil, fmt.Errorf("empty WS endpoint: %s", string(r))
	}

	ws := joinResp.Endpoint + "&platform=WEB&appVersion=1.1&version=6&device=browser&capabilities=0&clientType=VK&tgt=join"
	return ws, vkJoinLink, okJoinLink, r, nil
}

// Connect establishes the WebSocket connection to VK's signaling server
func (s *VKSignaling) Connect(ctx context.Context, wsURL string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	headers := http.Header{}
	headers.Add("Origin", "https://vk.com")
	headers.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("WS dial failed: %v", err)
	}
	s.wsConn = conn

	log.Printf("Connected to VK Signaling WS: %s", wsURL)

	// Configure initial media settings expected by VK
	s.vkSend("change-media-settings", map[string]interface{}{
		"mediaSettings": map[string]interface{}{
			"isAudioEnabled": false, "isVideoEnabled": true,
			"isScreenSharingEnabled": false, "isFastScreenSharingEnabled": false,
			"isAudioSharingEnabled": false, "isAnimojiEnabled": false,
		},
	})

	// Start reading loop
	go s.readLoop(ctx)

	return nil
}

// vkSend wraps commands correctly with sequence counters
func (s *VKSignaling) vkSend(command string, extra map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsConn == nil {
		return
	}
	s.vkSeq++

	var out []byte
	if pid, ok := extra["participantId"]; ok {
		dataJSON, _ := json.Marshal(extra["data"])
		out = []byte(fmt.Sprintf(`{"command":%q,"sequence":%d,"participantId":%v,"data":%s}`,
			command, s.vkSeq, pid, dataJSON))
	} else {
		extra["command"] = command
		extra["sequence"] = s.vkSeq
		out, _ = json.Marshal(extra)
	}
	s.wsConn.WriteMessage(websocket.TextMessage, out)
}

func (s *VKSignaling) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if s.wsConn == nil {
			return
		}

		_, msg, err := s.wsConn.ReadMessage()
		if err != nil {
			log.Printf("VK WS read error: %v", err)
			return
		}

		if string(msg) == "ping" {
			s.mu.Lock()
			s.wsConn.WriteMessage(websocket.TextMessage, []byte("pong"))
			s.mu.Unlock()
			continue
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(msg, &parsed); err != nil {
			continue
		}

		msgType, _ := parsed["type"].(string)
		if msgType == "notification" {
			s.mu.Lock()
			handler := s.onNotif
			s.mu.Unlock()
			if handler != nil {
				handler(parsed)
			}
			notif, _ := parsed["notification"].(string)
			switch notif {
			case "topology-changed":
				topo, _ := parsed["topology"].(string)
				s.EventChan <- map[string]interface{}{
					"type": "topology",
					"mode": strings.ToUpper(topo),
				}
			case "transmitted-data":
				data, ok := parsed["data"].(map[string]interface{})
				if ok {
					if candidate, ok := data["candidate"]; ok {
						s.EventChan <- map[string]interface{}{
							"type": "candidate",
							"data": candidate,
						}
					}
					if sdp, ok := data["sdp"].(map[string]interface{}); ok {
						s.EventChan <- map[string]interface{}{
							"type": "sdp",
							"data": sdp,
						}
					}
				}
			}
		}
	}
}

// SendMessage sends raw JSON payload to the websocket
func (s *VKSignaling) SendMessage(msg map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wsConn == nil {
		return fmt.Errorf("ws disconnected")
	}

	return s.wsConn.WriteJSON(msg)
}

func (s *VKSignaling) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsConn != nil {
		s.wsConn.Close()
		s.wsConn = nil
	}
}
