package tunnel

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vk-vpn/client/logx"
	"github.com/vk-vpn/client/socks"
)

type udpClient struct {
	udpConn    *net.UDPConn
	clientAddr *net.UDPAddr
	socksHdr   []byte
}

type relayTCP struct {
	conn net.Conn
	addr string
	rx   int64
}

type RelayBridge struct {
	tunnel     DataTunnel
	conns      sync.Map
	udpClients sync.Map
	nextID     atomic.Uint32
	logFn      func(string, ...interface{})
	mode       string
	readBuf    int
	ready      chan struct{}
	once       sync.Once
	socksUser  string
	socksPass  string

	listenerMu sync.Mutex
	listener   net.Listener
	closed     atomic.Bool
	onPong     func()
}

// SetOnPong is called when a control MsgPong arrives (joiner watchdog).
func (rb *RelayBridge) SetOnPong(fn func()) { rb.onPong = fn }

// SendControl sends a framed control message (ping/pong/config).
func (rb *RelayBridge) SendControl(msgType byte, payload []byte) {
	rb.send(ControlConnID, msgType, payload)
}

func NewRelayBridge(tunnel DataTunnel, mode string, readBuf int, logFn func(string, ...interface{})) *RelayBridge {
	if readBuf <= 0 {
		readBuf = defaultDCReadBuf
	}
	rb := &RelayBridge{
		tunnel:  tunnel,
		logFn:   logFn,
		mode:    mode,
		readBuf: readBuf,
		ready:   make(chan struct{}),
	}
	if logFn == nil {
		rb.logFn = func(string, ...interface{}) {}
	}
	tunnel.SetOnData(rb.handleTunnelData)
	tunnel.SetOnClose(rb.Close)
	return rb
}

func (rb *RelayBridge) MarkReady() {
	rb.once.Do(func() { close(rb.ready) })
}

func (rb *RelayBridge) send(connID uint32, msgType byte, payload []byte) {
	rb.tunnel.SendData(EncodeFrame(connID, msgType, payload))
}

func (rb *RelayBridge) handleTunnelData(data []byte) {
	DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
		if connID == ControlConnID {
			switch msgType {
			case MsgConfig:
				fps, batch, ok := DecodeVP8Config(payload)
				if !ok {
					return
				}
				if rb.mode == "creator" {
					rb.logFn("relay: peer vp8 config fps=%d batch=%d", fps, batch)
					rb.tunnel.Reconfigure(fps, batch)
				}
				return
			case MsgPing:
				if rb.mode == "creator" {
					rb.send(ControlConnID, MsgPong, nil)
				}
				return
			case MsgPong:
				if rb.mode == "joiner" && rb.onPong != nil {
					rb.onPong()
				}
				return
			}
		}
		switch rb.mode {
		case "joiner":
			rb.handleJoinerMessage(connID, msgType, payload)
		case "creator":
			rb.handleCreatorMessage(connID, msgType, payload)
		}
	})
}

func (rb *RelayBridge) handleJoinerMessage(connID uint32, msgType byte, payload []byte) {
	if msgType == MsgUDPReply {
		uval, ok := rb.udpClients.Load(connID)
		if !ok {
			return
		}
		uc := uval.(*udpClient)
		reply := make([]byte, len(uc.socksHdr)+len(payload))
		copy(reply, uc.socksHdr)
		copy(reply[len(uc.socksHdr):], payload)
		uc.udpConn.WriteToUDP(reply, uc.clientAddr)
		rb.udpClients.Delete(connID)
		return
	}
	val, ok := rb.conns.Load(connID)
	if !ok {
		return
	}
	sc := val.(*socksConn)
	switch msgType {
	case MsgConnectOK:
		select {
		case sc.rdy <- nil:
		default:
		}
	case MsgConnectErr:
		select {
		case sc.rdy <- fmt.Errorf("%s", payload):
		default:
		}
	case MsgData:
		sc.lastDataNs.Store(time.Now().UnixNano())
		sc.conn.Write(payload)
	case MsgClose:
		go rb.closeJoinerAfterInboundDrain(sc, connID)
	case MsgPing, MsgPong:
		// handled at control connID
	}
}

func (rb *RelayBridge) creatorTCPCount() int {
	n := 0
	rb.conns.Range(func(_, val any) bool {
		if _, ok := val.(*relayTCP); ok {
			n++
		}
		return true
	})
	return n
}

func (rb *RelayBridge) handleCreatorMessage(connID uint32, msgType byte, payload []byte) {
	switch msgType {
	case MsgConnect:
		if lim := RelayConnectLimitFromEnv(); lim > 0 && rb.creatorTCPCount() >= lim {
			rb.logFn("relay: connect limit %d, reject id=%d %s", lim, connID, payload)
			rb.send(connID, MsgConnectErr, []byte("parallel connect limit"))
			return
		}
		go rb.connectTCP(connID, string(payload))
	case MsgUDP:
		go rb.handleUDP(connID, payload)
	case MsgData:
		val, ok := rb.conns.Load(connID)
		if !ok {
			return
		}
		if rc, ok := val.(*relayTCP); ok {
			rc.conn.Write(payload)
		} else if c, ok := val.(net.Conn); ok {
			c.Write(payload)
		}
	case MsgClose:
		rb.closeRelayTCP(connID)
	}
}

// closeRelayTCP closes a creator-side origin TCP once (MsgClose + connectTCP EOF share this path).
func (rb *RelayBridge) closeRelayTCP(connID uint32) bool {
	val, ok := rb.conns.LoadAndDelete(connID)
	if !ok {
		return false
	}
	switch v := val.(type) {
	case *relayTCP:
		v.conn.Close()
		logx.DCClose(connID, v.addr, 0, v.rx)
	case net.Conn:
		v.Close()
	}
	return true
}

func (rb *RelayBridge) handleUDP(connID uint32, payload []byte) {
	if len(payload) < 2 {
		return
	}
	addrLen := int(payload[0])
	if addrLen == 0 || len(payload) < 1+addrLen {
		return
	}
	addr := string(payload[1 : 1+addrLen])
	data := payload[1+addrLen:]
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write(data)
	buf := make([]byte, socks.UDPBufSize)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	rb.send(connID, MsgUDPReply, buf[:n])
}

func (rb *RelayBridge) closeJoinerAfterInboundDrain(sc *socksConn, connID uint32) {
	grace := RelayInboundGraceFromEnv()
	idle := RelayInboundIdleFromEnv()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		last := sc.lastDataNs.Load()
		if last == 0 || time.Since(time.Unix(0, last)) >= idle {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	sc.conn.Close()
	rb.conns.Delete(connID)
	if _, ok := rb.tunnel.(OutboundQueued); ok {
		rb.logFn("relay: joiner close id=%d after inbound grace", connID)
	}
}

func (rb *RelayBridge) connectTCP(connID uint32, addr string) {
	if RelayAddrUnroutable(addr) {
		logx.DCConnectFail(connID, addr, fmt.Errorf("unroutable address"))
		rb.send(connID, MsgConnectErr, []byte("unroutable address"))
		return
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		logx.DCConnectFail(connID, addr, err)
		rb.send(connID, MsgConnectErr, []byte(err.Error()))
		return
	}
	logx.DCOpen(connID, addr)
	rc := &relayTCP{conn: conn, addr: addr}
	rb.conns.Store(connID, rc)
	rb.send(connID, MsgConnectOK, nil)

	buf := make([]byte, rb.readBuf)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			rc.rx += int64(n)
			rb.send(connID, MsgData, buf[:n])
		}
		if err != nil {
			break
		}
	}

	pending := WaitOutboundDrain(rb.tunnel, RelayDrainTimeoutFromEnv())
	if pending > 0 {
		rb.logFn("relay: close id=%d %s rx=%d vp8_pending=%d (drain timeout)", connID, addr, rc.rx, pending)
	} else if _, ok := rb.tunnel.(OutboundQueued); ok {
		rb.logFn("relay: close id=%d %s rx=%d vp8_pending=0 flushed=1", connID, addr, rc.rx)
	}

	rb.send(connID, MsgClose, nil)
	rb.closeRelayTCP(connID)
}

type socksConn struct {
	id         uint32
	conn       net.Conn
	rb         *RelayBridge
	rdy        chan error
	lastDataNs atomic.Int64
}

func (rb *RelayBridge) Close() {
	if !rb.closed.CompareAndSwap(false, true) {
		return
	}
	rb.listenerMu.Lock()
	ln := rb.listener
	rb.listener = nil
	rb.listenerMu.Unlock()
	if ln != nil {
		ln.Close()
	}
	rb.conns.Range(func(key, _ any) bool {
		rb.closeRelayTCP(key.(uint32))
		return true
	})
}

func (rb *RelayBridge) ListenSOCKS(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	rb.listenerMu.Lock()
	rb.listener = ln
	rb.listenerMu.Unlock()
	rb.logFn("relay: SOCKS5 on %s (mode=%s)", addr, rb.mode)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if rb.closed.Load() {
				return nil
			}
			continue
		}
		go rb.handleSOCKS(conn)
	}
}

func (rb *RelayBridge) handleSOCKS(conn net.Conn) {
	<-rb.ready
	if rb.closed.Load() {
		conn.Close()
		return
	}
	buf := make([]byte, socks.HandshakeBuf)
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != socks.Ver {
		conn.Close()
		return
	}
	if !socks.NegotiateAuth(conn, buf, n, rb.socksUser, rb.socksPass) {
		conn.Close()
		return
	}
	n, err = conn.Read(buf)
	if err != nil || n < 7 || buf[0] != socks.Ver {
		conn.Close()
		return
	}
	if buf[1] != socks.CmdTCP {
		conn.Write(socks.CmdErr)
		conn.Close()
		return
	}
	host, _, err := socks.ParseAddress(buf, n)
	if err != nil {
		conn.Write(socks.AddrErr)
		conn.Close()
		return
	}

	id := rb.nextID.Add(1)
	sc := &socksConn{id: id, conn: conn, rb: rb, rdy: make(chan error, 1)}
	rb.conns.Store(id, sc)
	rb.send(id, MsgConnect, []byte(host))

	select {
	case rdyErr := <-sc.rdy:
		if rdyErr != nil {
			conn.Write(socks.ConnFail)
			conn.Close()
			rb.conns.Delete(id)
			return
		}
	case <-time.After(20 * time.Second):
		conn.Write(socks.ConnFail)
		conn.Close()
		rb.conns.Delete(id)
		return
	}
	conn.Write(socks.OK)

	go func() {
		readBuf := make([]byte, rb.readBuf)
		for {
			rn, rerr := conn.Read(readBuf)
			if rn > 0 {
				rb.send(id, MsgData, readBuf[:rn])
			}
			if rerr != nil {
				rb.send(id, MsgClose, nil)
				rb.conns.Delete(id)
				return
			}
		}
	}()
}
