package tunnel

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// vp8Addr is a placeholder net.Addr for KCP. KCP-go's UDPSession needs a
// remote-address handle, but our transport doesn't route on IP — VP8 RTP
// frames flow through the pion-managed media track. The address value is
// never inspected by KCP except for equality, so a constant is enough.
type vp8Addr struct{}

func (vp8Addr) Network() string { return "vp8" }
func (vp8Addr) String() string  { return "vp8://peer" }

// errPacketConnClosed is returned by Read/Write after Close. Distinct from
// the i/o-timeout sentinel because kcp-go treats them differently — a
// closed conn must abort the session, a timeout must just yield.
var errPacketConnClosed = errors.New("vp8PacketConn: closed")

// kcpTimeoutErr satisfies the net.Error interface that KCP-go probes to
// decide whether to back off or give up.
type kcpTimeoutErr struct{}

func (kcpTimeoutErr) Error() string   { return "vp8PacketConn: i/o timeout" }
func (kcpTimeoutErr) Timeout() bool   { return true }
func (kcpTimeoutErr) Temporary() bool { return true }

// vp8PacketConn adapts the obfuscated VP8 track transport to net.PacketConn,
// so kcp-go's UDPSession can run on top of it without knowing it's not a
// real UDP socket.
//
//	WriteTo(packet, _) — KCP wants to send a packet. We pipe it through
//	                     the caller-provided sendFn, which is expected to
//	                     do obfuscator.EncodeData(packet) + WriteSample.
//	ReadFrom(buf)      — KCP wants the next inbound packet. We block on
//	                     recvCh until either a packet arrives (fed in by
//	                     the VP8 reassembler via deliver()) or a deadline
//	                     fires.
//
// We never close the underlying VP8 track here — its lifecycle is owned by
// the surrounding KCPTunnel.
type vp8PacketConn struct {
	sendFn func([]byte) error
	recvCh chan []byte

	closeOnce sync.Once
	closed    atomic.Bool
	closedCh  chan struct{}

	rdMu      sync.Mutex
	rdTimer   *time.Timer
	rdDeadlnP atomic.Pointer[time.Time]

	wrDeadlnP atomic.Pointer[time.Time]

	dropped atomic.Uint64 // recv-queue overflow counter (diagnostic)
}

// vp8PacketRecvCap is the bounded recv queue. KCP-go calls ReadFrom in a
// tight loop, so a small queue is usually empty. Bursts (VP8 reassembly
// catching up after a stall) need headroom — but if KCP can't drain in
// time, dropping is safe because the inner ARQ will retransmit anyway.
const vp8PacketRecvCap = 1024

func newVP8PacketConn(sendFn func([]byte) error) *vp8PacketConn {
	return &vp8PacketConn{
		sendFn:   sendFn,
		recvCh:   make(chan []byte, vp8PacketRecvCap),
		closedCh: make(chan struct{}),
	}
}

// deliver hands a decoded VP8 payload to whoever is currently blocked in
// ReadFrom. Called from the VP8 reassembler callback chain (HandleFrame
// → obfuscator.Decode → here). Never blocks; if the recv queue is full,
// the packet is dropped — KCP's selective-repeat ARQ will recover it.
func (c *vp8PacketConn) deliver(payload []byte) {
	if c.closed.Load() || len(payload) == 0 {
		return
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	select {
	case c.recvCh <- cp:
	default:
		c.dropped.Add(1)
	}
}

// DroppedCount returns the number of inbound packets that were dropped
// because the recv queue was full. Diagnostic only.
func (c *vp8PacketConn) DroppedCount() uint64 {
	return c.dropped.Load()
}

func (c *vp8PacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.closed.Load() {
		return 0, vp8Addr{}, errPacketConnClosed
	}

	// Compute a deadline channel only if SetReadDeadline was set. We use
	// a single shared timer (rdMu-guarded) to avoid allocating one per
	// call — KCP-go's ReadFrom is called at high rate.
	var deadline <-chan time.Time
	if dlPtr := c.rdDeadlnP.Load(); dlPtr != nil {
		dl := *dlPtr
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, vp8Addr{}, kcpTimeoutErr{}
			}
			c.rdMu.Lock()
			if c.rdTimer == nil {
				c.rdTimer = time.NewTimer(d)
			} else {
				if !c.rdTimer.Stop() {
					select {
					case <-c.rdTimer.C:
					default:
					}
				}
				c.rdTimer.Reset(d)
			}
			deadline = c.rdTimer.C
			c.rdMu.Unlock()
		}
	}

	select {
	case <-c.closedCh:
		return 0, vp8Addr{}, errPacketConnClosed
	case data := <-c.recvCh:
		n := copy(p, data)
		return n, vp8Addr{}, nil
	case <-deadline:
		return 0, vp8Addr{}, kcpTimeoutErr{}
	}
}

func (c *vp8PacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, errPacketConnClosed
	}
	if dlPtr := c.wrDeadlnP.Load(); dlPtr != nil {
		dl := *dlPtr
		if !dl.IsZero() && time.Now().After(dl) {
			return 0, kcpTimeoutErr{}
		}
	}
	// Copy on the way out — kcp-go may reuse the buffer after WriteTo
	// returns. Empirically this only matters when FEC parity packets
	// are involved, but cheap insurance.
	cp := make([]byte, len(p))
	copy(cp, p)
	if err := c.sendFn(cp); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *vp8PacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.closedCh)
	})
	return nil
}

func (c *vp8PacketConn) LocalAddr() net.Addr { return vp8Addr{} }

func (c *vp8PacketConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	_ = c.SetWriteDeadline(t)
	return nil
}

func (c *vp8PacketConn) SetReadDeadline(t time.Time) error {
	c.rdDeadlnP.Store(&t)
	return nil
}

func (c *vp8PacketConn) SetWriteDeadline(t time.Time) error {
	c.wrDeadlnP.Store(&t)
	return nil
}
