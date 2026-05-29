// Protocol-aware obfuscation layer for TURN ChannelData payloads.
//
// Goal: hide the DTLS / WebRTC signatures that on-path DPI uses to
// classify our session as a tunneled VPN (and shape it to ~1.3 Mbps)
// without breaking compatibility with the public VK TURN server. The
// public TURN server must continue to read STUN messages and
// ChannelData headers in plaintext to route packets — only the inner
// payload is masked.
//
// Wire behavior (per RFC 5389 STUN + RFC 8656 TURN):
//
//   First byte ∈ [0x00, 0x3F] → STUN message (Allocate, ChannelBind,
//   CreatePermission, Refresh, Binding). Passed through unchanged.
//
//   First byte ∈ [0x40, 0x7F] → TURN ChannelData. First 4 bytes
//   (channel number + length) preserved; bytes 4..N XOR'd against a
//   ChaCha20 keystream. Payload length is unchanged so the Length
//   field stays accurate.
//
// Symmetry: both peers must instantiate WrapPacketConn with the same
// key, derived from the shared join-link secret. The peer's wrapper
// applies the same XOR on receive, restoring plaintext before pion
// processes the frame.
//
// Crypto note (v1): fixed nonce derived once per session from the
// join-link salt. This is sufficient to defeat signature-based DPI
// (which does not perform per-stream cryptanalysis at deployment
// scale) while keeping wire format byte-for-byte length-preserving.
// The inner DTLS layer of WebRTC continues to provide full
// confidentiality and integrity. v2 will transmit an inline counter
// to close the KPA window if v1 results justify the wire overhead.
package webrtc

import (
	"crypto/sha256"
	"errors"
	"log"
	"net"
	"sync/atomic"

	"github.com/pion/ice/v2"
	"github.com/pion/logging"
	pion "github.com/pion/webrtc/v3"
	"golang.org/x/crypto/chacha20"
)

const (
	// wrapPayloadOffset is the size of a TURN ChannelData header per
	// RFC 8656 §12.4: ChannelNumber (2B) || Length (2B). Everything
	// from this offset onwards is the opaque inner payload that we
	// stream-cipher.
	wrapPayloadOffset = 4

	// wrapChannelDataMin/Max gate ChannelData frames by their first
	// byte. ChannelData top 2 bits = 01 → first byte ∈ [0x40, 0x7F].
	// STUN messages top 2 bits = 00 → first byte ∈ [0x00, 0x3F].
	wrapChannelDataMin = 0x40
	wrapChannelDataMax = 0x7F
)

// WrapPacketConn obfuscates TURN ChannelData payloads on a real UDP
// PacketConn. STUN traffic passes through untouched.
type WrapPacketConn struct {
	net.PacketConn
	key   [chacha20.KeySize]byte
	nonce [chacha20.NonceSize]byte

	wrappedOut atomic.Uint64 // ChannelData frames sent
	passOut    atomic.Uint64 // non-ChannelData frames sent (STUN etc.)
	wrappedIn  atomic.Uint64 // ChannelData frames received
	passIn     atomic.Uint64 // non-ChannelData frames received
}

// DeriveWrapKey derives the 32-byte ChaCha20 key and 12-byte nonce
// from a shared secret (the join-link token). Domain-separation tags
// ensure this key never collides with TunnelObfuscator keys derived
// from the same secret.
func DeriveWrapKey(secret []byte) ([chacha20.KeySize]byte, [chacha20.NonceSize]byte) {
	var key [chacha20.KeySize]byte
	var nonce [chacha20.NonceSize]byte
	k := sha256.Sum256(append([]byte("vk-vpn:turn-wrap:key:v1\x00"), secret...))
	n := sha256.Sum256(append([]byte("vk-vpn:turn-wrap:nonce:v1\x00"), secret...))
	copy(key[:], k[:])
	copy(nonce[:], n[:chacha20.NonceSize])
	return key, nonce
}

// NewWrapPacketConn returns a WrapPacketConn that mirrors the
// underlying connection except for the obfuscation applied to TURN
// ChannelData payloads.
func NewWrapPacketConn(inner net.PacketConn, secret []byte) (*WrapPacketConn, error) {
	if inner == nil {
		return nil, errors.New("wrap: nil inner PacketConn")
	}
	if len(secret) == 0 {
		return nil, errors.New("wrap: empty secret")
	}
	w := &WrapPacketConn{PacketConn: inner}
	w.key, w.nonce = DeriveWrapKey(secret)
	// Sanity: instantiate once so a misconfigured cipher fails at
	// construction rather than on the first packet.
	if _, err := chacha20.NewUnauthenticatedCipher(w.key[:], w.nonce[:]); err != nil {
		return nil, err
	}
	return w, nil
}

// xorPayload applies the ChaCha20 keystream in place. Stateless
// per call — a fresh cipher seeded with the same fixed nonce gives
// identical keystream for symmetric encrypt/decrypt.
func (w *WrapPacketConn) xorPayload(b []byte) {
	c, err := chacha20.NewUnauthenticatedCipher(w.key[:], w.nonce[:])
	if err != nil {
		return
	}
	c.XORKeyStream(b, b)
}

// isChannelData reports whether the packet's first byte indicates a
// TURN ChannelData frame.
func isChannelData(p []byte) bool {
	return len(p) > wrapPayloadOffset && p[0] >= wrapChannelDataMin && p[0] <= wrapChannelDataMax
}

func (w *WrapPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if isChannelData(p) {
		// Copy: pion may reuse the buffer after WriteTo returns, and
		// we must not mutate the caller's slice.
		out := make([]byte, len(p))
		copy(out, p)
		w.xorPayload(out[wrapPayloadOffset:])
		w.wrappedOut.Add(1)
		if _, err := w.PacketConn.WriteTo(out, addr); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	w.passOut.Add(1)
	return w.PacketConn.WriteTo(p, addr)
}

func (w *WrapPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := w.PacketConn.ReadFrom(p)
	if err != nil || n == 0 {
		return n, addr, err
	}
	if isChannelData(p[:n]) {
		w.xorPayload(p[wrapPayloadOffset:n])
		w.wrappedIn.Add(1)
	} else {
		w.passIn.Add(1)
	}
	return n, addr, err
}

// Stats returns rolling counters for diagnostics.
func (w *WrapPacketConn) Stats() (wrappedOut, passOut, wrappedIn, passIn uint64) {
	return w.wrappedOut.Load(), w.passOut.Load(),
		w.wrappedIn.Load(), w.passIn.Load()
}

// InstallTURNWrapMux attaches a wrapped UDP socket as the ICE UDP mux
// on the given SettingEngine, returning the live wrapper so callers
// can pull diagnostics. On the diag/ru-vps-test branch the wrapper
// is always installed when a secret is supplied — the env-gate that
// lives on main (VK_VPN_TURN_WRAP=0) is intentionally absent so the
// test runs against the exact baseline we observe in production.
//
// The underlying UDP socket binds to 0.0.0.0:0 — the kernel routing
// table picks the egress interface at send time. We intentionally do
// not bind to a specific interface IP because Windows route changes
// (split-tunnel install / rollback) would otherwise break the bound
// socket — the same brittleness that retired the original
// SetICEUDPMux usage on the client.
func InstallTURNWrapMux(se *pion.SettingEngine, secret []byte, who string) (*WrapPacketConn, error) {
	if se == nil {
		return nil, errors.New("wrap: nil SettingEngine")
	}
	if len(secret) == 0 {
		log.Printf("[%s] turn-wrap: REQUESTED but no secret available — wrapper not installed", who)
		return nil, nil
	}

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	wrap, err := NewWrapPacketConn(udp, secret)
	if err != nil {
		udp.Close()
		return nil, err
	}
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{
		Logger:  logging.NewDefaultLoggerFactory().NewLogger("ice-udp-mux"),
		UDPConn: wrap,
	})
	se.SetICEUDPMux(mux)
	log.Printf("[%s] turn-wrap: installed (local=%s, ChaCha20 stream, fixed nonce v1)",
		who, udp.LocalAddr())
	return wrap, nil
}
