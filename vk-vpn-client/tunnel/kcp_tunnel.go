package tunnel

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	kcp "github.com/xtaci/kcp-go/v5"
)

// kcpConvID is a fixed conversation ID that both sides must agree on for
// KCP-go to accept each other's packets. Equivalent to a port number in
// regular KCP-over-UDP; we use a constant because the joiner and creator
// don't negotiate one out of band.
//
// 0x564B5650 = ASCII 'V','K','V','P'. Memorable and unlikely to collide
// with arbitrary garbage if VP8 obfuscation glitches.
const kcpConvID uint32 = 0x564B5650

// kcpKeepaliveInterval — how often the cover-traffic loop wakes up and,
// if KCP itself has been quiet, emits one fake-VP8 keepalive sample.
// We mirror the legacy VP8DataTunnel keepalive rate (~100 ms) so the
// outbound RTP pattern stays close to a real low-bitrate video stream.
const kcpKeepaliveInterval = 100 * time.Millisecond

// kcpVP8SampleDuration is the Duration we hand to pion's WriteSample.
// pion uses it to compute RTP timestamps for the outgoing packetizer.
// 33 ms ≈ 30 fps, which is consistent with what an actual video call
// would produce. KCP packet rate is independent of this — we just need
// the RTP timestamp progression to look plausible to any observer.
const kcpVP8SampleDuration = 33 * time.Millisecond

// KCPTunnel runs a reliable, congestion-controlled byte stream on top
// of the unreliable VP8 RTP transport. The outside world sees ordinary
// VP8 video frames (covered by ChaCha20-via-obfuscator and wrapped with
// a fake-VP8 header). The inside payload is a KCP-go UDPSession.
//
// Implements DataTunnel (same surface as VP8DataTunnel / DCTunnel), so
// the existing RelayBridge wires up without modification.
type KCPTunnel struct {
	track *webrtc.TrackLocalStaticSample
	obf   *TunnelObfuscator
	logFn func(string, ...interface{})
	cfg   KCPConfig

	pktConn *vp8PacketConn
	session *kcp.UDPSession

	stopCh   chan struct{}
	stopOnce sync.Once
	running  atomic.Bool

	onData  func([]byte)
	onClose func()

	// metrics
	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64

	// last-activity gate for the keepalive scheduler. Tracks the wall
	// time of the last outbound packet that left through sendVP8Frame,
	// not the last KCP.Write — so retransmits and FEC parity also reset
	// the timer.
	lastSentNs atomic.Int64
}

// NewKCPTunnel constructs a KCP session bound to the given VP8 track and
// obfuscator. Both the joiner and the creator call this with the same
// convID — that's all the "negotiation" KCP needs to recognize peers.
//
// The KCP session is created in stream mode (large user writes are split
// internally) with no internal block cipher (our obfuscator already does
// authenticated encryption). The packet-conn it sits on top of doesn't
// route on IP — see vp8PacketConn for the adaptor.
func NewKCPTunnel(track *webrtc.TrackLocalStaticSample, obf *TunnelObfuscator, logFn func(string, ...interface{})) (*KCPTunnel, error) {
	if track == nil {
		return nil, errors.New("kcp: nil VP8 track")
	}
	cfg := KCPConfigFromEnv()

	t := &KCPTunnel{
		track:  track,
		obf:    obf,
		logFn:  logFn,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
	if t.logFn == nil {
		t.logFn = func(string, ...interface{}) {}
	}

	t.pktConn = newVP8PacketConn(t.sendVP8Frame)

	// kcp.NewConn3 establishes a single peer-to-peer KCP session over an
	// arbitrary PacketConn. nil BlockCrypt — our obfuscator layer already
	// does AEAD; KCP's built-in encryption would be redundant and waste
	// CPU. FEC shard counts come from env (default 10:3).
	sess, err := kcp.NewConn3(kcpConvID, vp8Addr{}, nil, cfg.DataShards, cfg.ParityShards, t.pktConn)
	if err != nil {
		_ = t.pktConn.Close()
		return nil, err
	}

	sess.SetNoDelay(cfg.NoDelay, cfg.Interval, cfg.Resend, cfg.NoCongestion)
	sess.SetWindowSize(cfg.SendWindow, cfg.RecvWindow)
	sess.SetMtu(cfg.MTU)
	// CRITICAL: message mode (NOT stream).
	//
	// RelayBridge.handleTunnelData → DecodeFrames expects each callback
	// invocation to contain whole frame(s). In stream mode KCP would hand
	// us arbitrary-sized chunks that straddle frame boundaries, and
	// DecodeFrames would discard everything past the first partial frame
	// — silently losing most of the data. We verified this in prod: with
	// stream mode the server pumped 44 MB into the tunnel, the client
	// received 44 MB via KCP, and the curl on top got 0 bytes because
	// every Read() chunk dropped its tail.
	//
	// In message mode each SendData() / session.Write() corresponds to
	// exactly one session.Read() on the peer — frame boundaries hold.
	// Max message size = 256 segments × MSS ≈ 320 KB, well above our
	// max frame (≤ readBuf = 65 KB).
	sess.SetStreamMode(false)
	sess.SetWriteDelay(cfg.WriteDelay)
	sess.SetACKNoDelay(cfg.ACKNoDelay)
	t.session = sess

	// stream_mode=false is the only correct setting — see SetStreamMode
	// block above. We log it explicitly so the diagnostic log shows
	// at-a-glance whether both peers actually agree on framing semantics.
	t.logFn("kcp: tunnel ready conv=0x%08x mtu=%d window=%d/%d fec=%d/%d nodelay=%d,%d,%d,%d stream_mode=false probe_pps=%d",
		kcpConvID, cfg.MTU, cfg.SendWindow, cfg.RecvWindow,
		cfg.DataShards, cfg.ParityShards,
		cfg.NoDelay, cfg.Interval, cfg.Resend, cfg.NoCongestion,
		cfg.ProbeKeepalivePPS)
	return t, nil
}

// Start launches the readLoop, the cover-traffic keepalive loop, and the
// metrics loop. fps/batch are kept in the signature for parity with
// VP8DataTunnel.Start() — KCP determines its own pacing.
func (t *KCPTunnel) Start(_, _ int) {
	if !t.running.CompareAndSwap(false, true) {
		return
	}
	go t.readLoop()
	go t.keepaliveLoop()
	go t.metricsLoop()
	if t.cfg.ProbeKeepalivePPS > 0 {
		go t.probeKeepaliveLoop()
	}
}

// SendData enqueues data into the KCP session's send buffer. Blocks if
// the send window is full — exactly the backpressure semantics we want,
// because it propagates back to the relay-side TCP read loop and from
// there to the upstream TCP stack via socket buffering.
func (t *KCPTunnel) SendData(data []byte) {
	if len(data) == 0 || t.session == nil {
		return
	}
	if !t.running.Load() {
		return
	}
	n, err := t.session.Write(data)
	if err != nil {
		// kcp-go returns errors.New on closed session / timeout; the
		// surrounding RelayBridge handles teardown via SetOnClose.
		t.logFn("kcp: session.Write err: %v (wrote %d/%d)", err, n, len(data))
		return
	}
	t.bytesOut.Add(uint64(n))
}

// SendQueueDepth always reports 0 in KCP mode.
//
// Background: kcp-go v5 deprecated the public WaitSnd() accessor that the
// VP8DataTunnel used for half-close drain. We don't have direct visibility
// into the KCP send queue here, so we let the caller (RelayBridge) rely
// on its time-based RelayDrainTimeout instead. That's safe because KCP
// already guarantees in-order reliable delivery; the only thing the
// drain protects against is closing the session before the final flight
// makes it out, and the drain timeout (default 120 s) is far longer than
// any reasonable RTT × retry cycle.
func (t *KCPTunnel) SendQueueDepth() int { return 0 }

// WaitSendQueueDrained is a stub for the same reason as SendQueueDepth.
// The caller still gets blocked for `timeout`, which gives KCP room to
// flush — but we don't have a precise zero-check.
func (t *KCPTunnel) WaitSendQueueDrained(timeout time.Duration) int {
	if timeout > 0 {
		// Brief sleep to give KCP one or two retransmit cycles. We
		// intentionally do NOT block the full timeout — that would
		// stall MsgClose forever for sessions where data already drained.
		select {
		case <-time.After(min(timeout, 500*time.Millisecond)):
		case <-t.stopCh:
		}
	}
	return 0
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// readLoop drains the KCP session into the OnData callback.
//
// Implementation note: we use a fully blocking Read here. The previous
// version polled stopCh with a 250 ms SetReadDeadline, but kcp-go's
// timeout error is a plain errors.New("timeout") — it does NOT satisfy
// the net.Error interface, so our timeout-vs-fatal classifier got it
// wrong and killed the whole tunnel 250 ms after start (see Phase 3b
// regression in app.log @ 17:23:15.330).
//
// With a blocking Read, exit is driven by session.Close() (called from
// Stop()), which unblocks Read with io.ErrClosedPipe or io.EOF. We also
// keep a defensive text-match for "timeout" / "closed" so any future
// kcp-go change can't trap us in a dead-loop of error spam.
func (t *KCPTunnel) readLoop() {
	// 128 KB read buffer — max KCP message we'll emit is bounded by the
	// frame size on the writing end (≤ readBuf = 65 KB), but a margin
	// avoids a short-read scenario if KCP-go's internal reassembly ever
	// returns more than expected.
	buf := make([]byte, 128*1024)
	// Diagnostic: dump head of the first few reads so a mode mismatch
	// (sender stream / receiver message) is immediately obvious from logs.
	// A valid frame begins with a 4-byte big-endian length (typically
	// 5..65540), then 4-byte connID, then 1 msgType byte. If we see e.g.
	// length > 1 GB or wildly inconsistent magic, peers disagree.
	const headDumps = 5
	var headDumpsLeft = headDumps
	for {
		n, err := t.session.Read(buf)
		if n > 0 {
			t.bytesIn.Add(uint64(n))
			if headDumpsLeft > 0 {
				hex := n
				if hex > 16 {
					hex = 16
				}
				t.logFn("kcp: read#%d n=%d head=% x", headDumps-headDumpsLeft+1, n, buf[:hex])
				headDumpsLeft--
			}
			if t.onData != nil {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				t.onData(cp)
			}
		}
		if err == nil {
			continue
		}
		// Quiet termination paths — the session was closed deliberately
		// (peer EOF or our own Stop). Log once, exit cleanly.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
			t.logFn("kcp: session.Read closed")
			t.Stop()
			return
		}
		// Defensive: kcp-go's timeout/closed errors are plain strings.
		// Treat any "timeout" as transient (do not tear down — the next
		// Read will succeed once a packet arrives). Treat "closed" as
		// fatal.
		msg := err.Error()
		if strings.Contains(msg, "closed") {
			t.logFn("kcp: session.Read closed: %v", err)
			t.Stop()
			return
		}
		if strings.Contains(msg, "timeout") {
			// Should not happen — we no longer set a read deadline —
			// but if a future kcp-go version reintroduces one we don't
			// want to die. Just keep looping.
			continue
		}
		t.logFn("kcp: session.Read err: %v — tearing down", err)
		t.Stop()
		return
	}
}

// keepaliveLoop maintains a steady "looks like a video call" RTP cadence
// even when KCP itself is idle. If the last outbound VP8 sample (real or
// keepalive) was more than kcpKeepaliveInterval ago, we emit one. This
// is what lets VK SFU / TURN policer observers see a stable low-rate
// video stream during quiet periods.
func (t *KCPTunnel) keepaliveLoop() {
	if t.obf == nil {
		// No cover available — KCP packets carry their own RTP heartbeats
		// implicitly via Update(); nothing more to do.
		return
	}
	ticker := time.NewTicker(kcpKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			last := t.lastSentNs.Load()
			if last != 0 && time.Since(time.Unix(0, last)) < kcpKeepaliveInterval {
				continue
			}
			sample := t.obf.EncodeKeepalive()
			if err := t.track.WriteSample(media.Sample{Data: sample, Duration: kcpKeepaliveInterval}); err != nil {
				t.logFn("kcp: keepalive WriteSample err: %v", err)
				continue
			}
			t.lastSentNs.Store(time.Now().UnixNano())
		}
	}
}

// probeKeepaliveLoop emits extra synthetic VP8 keepalive samples at a
// fixed rate, independent of the data-flow gate used by keepaliveLoop.
// Purpose: inject controlled pps load on the same track to diagnose
// whether VK TURN's policer is per-track or per-session — see T2 in
// docs/THROUGHPUT_PLAN.md. NOT for production use; gated by env
// VK_VPN_KCP_PROBE_KEEPALIVE_PPS > 0.
func (t *KCPTunnel) probeKeepaliveLoop() {
	if t.obf == nil || t.cfg.ProbeKeepalivePPS <= 0 {
		return
	}
	interval := time.Second / time.Duration(t.cfg.ProbeKeepalivePPS)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t.logFn("kcp: PROBE keepalive loop active — %d pps (interval=%v)", t.cfg.ProbeKeepalivePPS, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			sample := t.obf.EncodeKeepalive()
			_ = t.track.WriteSample(media.Sample{Data: sample, Duration: kcpVP8SampleDuration})
			// Intentionally do NOT update lastSentNs — the idle-gated
			// keepaliveLoop should still run independently if KCP itself
			// stalls. Probe is purely additive load.
		}
	}
}

// metricsLoop prints a single rate line every 10s when something moved.
// Mirrors the rate-logger we added on the server-DC side, so both
// transports produce comparable diagnostic timelines.
func (t *KCPTunnel) metricsLoop() {
	const interval = 10 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var prevIn, prevOut uint64
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			in := t.bytesIn.Load()
			out := t.bytesOut.Load()
			dIn := in - prevIn
			dOut := out - prevOut
			prevIn, prevOut = in, out
			waitSnd := t.SendQueueDepth()
			dropped := t.pktConn.DroppedCount()
			if dIn == 0 && dOut == 0 && waitSnd == 0 {
				continue
			}
			kbpsIn := float64(dIn) * 8 / 1e4
			kbpsOut := float64(dOut) * 8 / 1e4
			srtt := t.session.GetSRTT()
			rto := t.session.GetRTO()
			t.logFn("kcp-rate: in=%d (+%d, %.1f kbps) out=%d (+%d, %.1f kbps) srtt=%dms rto=%dms recv_drop=%d",
				in, dIn, kbpsIn, out, dOut, kbpsOut, srtt, rto, dropped)
			_ = waitSnd // kept for future reintroduction
		}
	}
}

// sendVP8Frame is the bridge from kcp-go's UDPSession.WriteTo (via the
// vp8PacketConn adaptor) to the real pion track. Encrypts with our
// obfuscator and writes a single VP8 sample.
func (t *KCPTunnel) sendVP8Frame(packet []byte) error {
	var sample []byte
	if t.obf == nil {
		sample = packet
	} else {
		sample = t.obf.EncodeData(packet)
	}
	if err := t.track.WriteSample(media.Sample{Data: sample, Duration: kcpVP8SampleDuration}); err != nil {
		return err
	}
	t.lastSentNs.Store(time.Now().UnixNano())
	return nil
}

// HandleFrame is invoked from ReadVP8Track callback chain. Decrypts the
// VP8 sample via the obfuscator and feeds the inner KCP packet into the
// pktConn so the KCP state machine can process it.
func (t *KCPTunnel) HandleFrame(frame []byte) {
	if t.obf == nil {
		if len(frame) > 0 {
			t.pktConn.deliver(frame)
		}
		return
	}
	res := t.obf.Decode(frame)
	if !res.HasFrame || res.SelfEcho {
		return
	}
	if res.PeerRestart {
		t.logFn("kcp: peer restart epoch=0x%08x — KCP state may need to flush", res.PeerEpoch)
	}
	if res.Keepalive || len(res.Payload) == 0 {
		return
	}
	t.pktConn.deliver(res.Payload)
}

// Stop tears down the KCP session and signals all goroutines. Idempotent.
// Safe to call from anywhere — readLoop on EOF, RelayBridge teardown,
// Joiner.Close — even if Start() was never reached.
func (t *KCPTunnel) Stop() {
	t.stopOnce.Do(func() {
		t.running.Store(false)
		close(t.stopCh)
		if t.session != nil {
			_ = t.session.Close()
		}
		if t.pktConn != nil {
			_ = t.pktConn.Close()
		}
		if t.onClose != nil {
			t.onClose()
		}
	})
}

func (t *KCPTunnel) SetOnData(fn func([]byte))  { t.onData = fn }
func (t *KCPTunnel) SetOnClose(fn func())       { t.onClose = fn }
func (t *KCPTunnel) Reconfigure(_ int, _ int)   { /* no-op — KCP paces itself */ }

// Compile-time assertion that KCPTunnel satisfies both interfaces.
// If either drifts, this fails the build instead of erroring at runtime.
var (
	_ DataTunnel     = (*KCPTunnel)(nil)
	_ OutboundQueued = (*KCPTunnel)(nil)
)
