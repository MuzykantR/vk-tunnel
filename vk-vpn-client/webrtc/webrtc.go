package webrtc

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/parser"
)

type Client struct {
	pc      *webrtc.PeerConnection
	dc      *webrtc.DataChannel
	ctx     context.Context
	payload *parser.VPNPayload
	wsConn  *websocket.Conn
	mu      sync.Mutex
	conn    net.Conn // Active SOCKS5/Tunnel connection
	vkSeq   int
	peerID  float64
}

func NewClient(ctx context.Context, payload *parser.VPNPayload) (*Client, error) {
	log.Printf("Initializing WebRTC Joiner for link: %s", payload.Link)

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("Client ICE Connection State: %s\n", state.String())
	})

	return &Client{
		pc:      pc,
		ctx:     ctx,
		payload: payload,
	}, nil
}

// sendVK is a helper to send formatted JSON commands to VK SFU
func (c *Client) sendVK(command string, data interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsConn == nil {
		return
	}
	c.vkSeq++
	msg := map[string]interface{}{
		"command":  command,
		"sequence": c.vkSeq,
	}
	if data != nil && c.peerID != 0 {
		msg["participantId"] = c.peerID
		msg["data"] = data
	}
	c.wsConn.WriteJSON(msg)
}

// Connect establishes real connection to VK SFU WebSocket and handles signaling
func (c *Client) Connect() error {
	log.Println("Setting up WebRTC tracks and DataChannels for VK SFU...")

	// Resolve the HTTP link to a WebSocket URL
	log.Println("Resolving join link via VK HTTP API...")
	wsURL, err := ResolveJoinLink(c.payload.Link)
	if err != nil {
		return err // Could be CAPTCHA_REQUIRED error
	}

	// 1. Create fake Audio Opus track
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio", "tunnel-audio",
	)
	if err == nil {
		c.pc.AddTrack(audioTrack)
	}

	// 2. Create fake Video VP8 track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video", "tunnel-video",
	)
	if err == nil {
		c.pc.AddTrack(videoTrack)
	}

	// 3. Create VK-specific DataChannels
	ordered := true
	c.pc.CreateDataChannel("producerNotification", &webrtc.DataChannelInit{Ordered: &ordered})
	c.pc.CreateDataChannel("producerCommand", &webrtc.DataChannelInit{Ordered: &ordered})
	c.pc.CreateDataChannel("producerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})
	c.pc.CreateDataChannel("consumerScreenShare", &webrtc.DataChannelInit{Ordered: &ordered})

	// 4. Create the actual tunnel DataChannel (Negotiated: true, ID: 2)
	negotiated := true
	dcID := uint16(2)
	dc, err := c.pc.CreateDataChannel("tunnel", &webrtc.DataChannelInit{
		Negotiated: &negotiated,
		ID:         &dcID,
	})
	if err != nil {
		return err
	}
	c.dc = dc

	c.dc.OnOpen(func() {
		log.Printf("Client DataChannel '%s' is open. VPN tunnel ready.", c.dc.Label())
	})

	c.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			conn.Write(msg.Data)
		}
	})

	// 5. Connect to VK WebSocket
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	wsConn, _, err := dialer.DialContext(c.ctx, wsURL, nil)
	if err != nil {
		return err
	}
	c.wsConn = wsConn
	log.Println("Connected to VK WebSocket successfully")

	// 6. Handle local ICE candidates
	c.pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || c.peerID == 0 {
			return
		}
		c.sendVK("transmit-data", map[string]interface{}{
			"candidate": candidate.ToJSON(),
		})
	})

	// 7. Start reading loop for VK JSON signaling
	go func() {
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

			var parsed map[string]interface{}
			if err := json.Unmarshal(msg, &parsed); err != nil {
				continue
			}

			msgType, _ := parsed["type"].(string)
			if msgType == "notification" {
				notif, _ := parsed["notification"].(string)
				
				// Capture Participant ID for sending back data
				if notif == "registered-peer" || notif == "transmitted-data" {
					if pid, ok := parsed["participantId"].(float64); ok {
						c.peerID = pid
					}
				}

				if notif == "transmitted-data" {
					data, _ := parsed["data"].(map[string]interface{})
					
					// Handle SDP Offer
					if sdpObj, ok := data["sdp"].(map[string]interface{}); ok {
						sdpType, _ := sdpObj["type"].(string)
						sdpStr, _ := sdpObj["sdp"].(string)

						if sdpType == "offer" {
							err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
								Type: webrtc.SDPTypeOffer,
								SDP:  sdpStr,
							})
							if err != nil {
								log.Printf("SetRemoteDescription error: %v", err)
								continue
							}

							ans, err := c.pc.CreateAnswer(nil)
							if err != nil {
								log.Printf("CreateAnswer error: %v", err)
								continue
							}
							c.pc.SetLocalDescription(ans)

							c.sendVK("transmit-data", map[string]interface{}{
								"sdp": map[string]interface{}{
									"type": "answer",
									"sdp":  ans.SDP,
								},
								"animojiVersion": 2,
							})
							log.Println("Sent SDP Answer back to VK SFU")
						}
					}

					// Handle Remote ICE Candidates
					if candObj, ok := data["candidate"]; ok {
						candJSON, _ := json.Marshal(candObj)
						var iceCand webrtc.ICECandidateInit
						if err := json.Unmarshal(candJSON, &iceCand); err == nil {
							c.pc.AddICECandidate(iceCand)
						}
					}
				}
			}
		}
	}()

	return nil
}

// HandleSOCKS5Conn bridges a local SOCKS5 TCP connection to the WebRTC tunnel.
func (c *Client) HandleSOCKS5Conn(conn net.Conn) {
	log.Printf("New local SOCKS5 connection from %s, bridging to WebRTC...", conn.RemoteAddr())

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	go func() {
		defer conn.Close()
		buf := make([]byte, 16*1024) // 16KB chunks
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("SOCKS5 read error: %v", err)
				}
				break
			}
			
			if c.dc != nil && c.dc.ReadyState() == webrtc.DataChannelStateOpen {
				err = c.dc.Send(buf[:n])
				if err != nil {
					log.Printf("DataChannel send error: %v", err)
					break
				}
			}
		}
	}()
}

func (c *Client) Close() {
	if c.pc != nil {
		c.pc.Close()
	}
	if c.wsConn != nil {
		c.wsConn.Close()
	}
}
