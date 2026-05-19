package creator

import (
	"encoding/json"
	"log"

	"github.com/pion/webrtc/v3"
)

const TopologyDirect = "DIRECT"

// P2PHandler — логика создателя из whitelist-bypass/headless/vk (только DIRECT).
type P2PHandler struct {
	bridge            *Bridge
	remotePeerID      *int64
	pendingOffer      *webrtc.SessionDescription
	pendingCandidates []webrtc.ICECandidateInit
	connected         bool
}

func NewP2PHandler(b *Bridge) *P2PHandler {
	return &P2PHandler{bridge: b}
}

func (p *P2PHandler) setupCallbacks() {
	p.bridge.session.SetOnICE(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		p.OnPionICECandidate(c.ToJSON())
	})
}

func (p *P2PHandler) Init() error {
	offer, err := p.bridge.session.CreateOffer()
	if err != nil {
		return err
	}
	p.pendingOffer = &offer
	log.Printf("[p2p] Offer ready, SDP len=%d", len(offer.SDP))
	return nil
}

func (p *P2PHandler) Reset() {
	log.Println("[p2p] Resetting session...")
	p.connected = false
	p.bridge.session.Close()
	sess, err := NewTunnelSession(p.bridge.iceServers)
	if err != nil {
		log.Printf("[p2p] reset session: %v", err)
		return
	}
	p.bridge.session = sess
	p.setupCallbacks()
	p.pendingOffer = nil
	p.pendingCandidates = nil
	if err := p.Init(); err != nil {
		log.Printf("[p2p] reset Init: %v", err)
	}
}

func (p *P2PHandler) OnRegisteredPeer(participantID int64) {
	old := p.remotePeerID
	log.Printf("[p2p] Peer registered: %d (topology=%s, old=%v)", participantID, p.bridge.topology, old)

	// Skip if same peer already registered — VK sends both participant-joined and registered-peer
	if old != nil && *old == participantID && p.pendingOffer == nil {
		log.Printf("[p2p] Same peer %d already registered, skipping duplicate", participantID)
		return
	}

	p.remotePeerID = &participantID

	if old != nil && *old != participantID && p.pendingOffer == nil {
		p.Reset()
	}
	p.sendOfferToPeer(participantID)
}

func (p *P2PHandler) OnTransmittedData(data map[string]interface{}) {
	if cand, ok := data["candidate"]; ok {
		b, _ := json.Marshal(cand)
		var c webrtc.ICECandidateInit
		if json.Unmarshal(b, &c) == nil {
			_ = p.bridge.session.AddICECandidate(c)
		}
	}
	if sdp, ok := data["sdp"].(map[string]interface{}); ok {
		sdpType, _ := sdp["type"].(string)
		sdpStr, _ := sdp["sdp"].(string)
		state := p.bridge.session.SignalingState().String()
		log.Printf("[p2p] Remote SDP: %s (signalingState=%s)", sdpType, state)
		switch sdpType {
		case "answer":
			if err := p.bridge.session.SetRemoteDescription(webrtc.SDPTypeAnswer, sdpStr); err != nil {
				log.Printf("[p2p] SetRemoteDescription(answer) failed: %v", err)
			}
		case "offer":
			// The joiner reaches the offerer role only when it asks for an ICE
			// restart. Reject the offer if we ourselves are still mid-negotiation.
			sigState := p.bridge.session.SignalingState()
			if sigState != webrtc.SignalingStateStable {
				log.Printf("[p2p] Ignoring offer — signaling state %s != stable", sigState.String())
				return
			}
			log.Println("[p2p] ICE restart offer received from joiner, answering")
			if err := p.bridge.session.SetRemoteDescription(webrtc.SDPTypeOffer, sdpStr); err != nil {
				log.Printf("[p2p] SetRemoteDescription(offer) failed: %v", err)
				return
			}
			ans, err := p.bridge.session.CreateAnswer()
			if err != nil {
				log.Printf("[p2p] CreateAnswer failed: %v", err)
				return
			}
			if p.remotePeerID != nil {
				p.bridge.vkSendTransmit(*p.remotePeerID, map[string]interface{}{
					"sdp": map[string]interface{}{"type": ans.Type.String(), "sdp": ans.SDP},
				})
				log.Println("[p2p] ICE restart answer sent")
			}
		}
	}
}

func (p *P2PHandler) OnPionICECandidate(init webrtc.ICECandidateInit) {
	if p.remotePeerID != nil {
		p.bridge.vkSendTransmit(*p.remotePeerID, map[string]interface{}{"candidate": init})
	} else {
		p.pendingCandidates = append(p.pendingCandidates, init)
	}
}

func (p *P2PHandler) sendOfferToPeer(participantID int64) {
	offer := p.pendingOffer
	candidates := p.pendingCandidates
	p.pendingOffer = nil
	p.pendingCandidates = nil

	if offer != nil {
		log.Printf("[p2p] Sending offer to peer %d", participantID)
		p.bridge.vkSendTransmit(participantID, map[string]interface{}{
			"sdp": map[string]interface{}{"type": offer.Type.String(), "sdp": offer.SDP},
		})
	}
	for _, c := range candidates {
		p.bridge.vkSendTransmit(participantID, map[string]interface{}{"candidate": c})
	}
}

func (p *P2PHandler) kickRemotePeer() {
	if p.remotePeerID != nil {
		p.bridge.vkSend("remove-participant", map[string]interface{}{
			"participantId": *p.remotePeerID,
			"ban":           false,
		})
	}
}
