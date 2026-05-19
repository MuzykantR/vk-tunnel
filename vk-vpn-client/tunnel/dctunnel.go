// DCTunnel — SCTP DataChannel I/O with optional ChaCha20 obfuscation (whitelist-bypass model).
package tunnel

import (
	"encoding/binary"
	"io"
	"log"
	"sync/atomic"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
)

const defaultDCReadBuf = 65536

type DCTunnel struct {
	dc      *webrtc.DataChannel
	raw     datachannel.ReadWriteCloser
	logFn   func(string, ...interface{})
	onData  func([]byte)
	onClose func()
	obf     *TunnelObfuscator
	readBuf int

	recvBytes atomic.Uint64
	sendBytes atomic.Uint64
}

func NewDCTunnel(dc *webrtc.DataChannel, obf *TunnelObfuscator, readBuf int, logFn func(string, ...interface{})) *DCTunnel {
	t := &DCTunnel{dc: dc, obf: obf, readBuf: readBuf, logFn: logFn}
	if readBuf <= 0 {
		t.readBuf = defaultDCReadBuf
	}
	if logFn == nil {
		t.logFn = func(string, ...interface{}) {}
	}

	raw, err := dc.Detach()
	if err != nil {
		log.Printf("dctunnel: detach failed, using callback mode: %v", err)
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			t.deliverMessage(msg.Data)
		})
		dc.OnClose(func() {
			if t.onClose != nil {
				t.onClose()
			}
		})
		return t
	}
	t.raw = raw
	go t.readLoop()
	return t
}

// NewDCTunnelFromRaw uses an already-detached ReadWriteCloser (joiner detaches first).
func NewDCTunnelFromRaw(dc *webrtc.DataChannel, raw datachannel.ReadWriteCloser, obf *TunnelObfuscator, readBuf int, logFn func(string, ...interface{})) *DCTunnel {
	t := &DCTunnel{dc: dc, raw: raw, obf: obf, readBuf: readBuf, logFn: logFn}
	if readBuf <= 0 {
		t.readBuf = defaultDCReadBuf
	}
	if logFn == nil {
		t.logFn = func(string, ...interface{}) {}
	}
	go t.readLoop()
	return t
}

func (t *DCTunnel) readLoop() {
	buf := make([]byte, t.readBuf)
	for {
		n, isString, err := t.raw.ReadDataChannel(buf)
		if err != nil {
			if err != io.EOF {
				t.logFn("dctunnel: read error: %v", err)
			}
			if t.onClose != nil {
				t.onClose()
			}
			return
		}
		if isString || n == 0 {
			continue
		}
		t.recvBytes.Add(uint64(n))
		cp := make([]byte, n)
		copy(cp, buf[:n])
		t.deliverMessage(cp)
	}
}

func (t *DCTunnel) deliverMessage(data []byte) {
	if len(data) == 0 {
		return
	}
	if t.obf != nil {
		pt, ok := t.obf.DecryptPayload(data)
		if !ok {
			t.logFn("dctunnel: decrypt failed, drop %d bytes", len(data))
			return
		}
		data = pt
	}
	if t.onData == nil {
		return
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(data)))
	copy(frame[4:], data)
	t.onData(frame)
}

func (t *DCTunnel) sendRaw(data []byte) {
	if t.raw != nil {
		t.sendBytes.Add(uint64(len(data)))
		_, _ = t.raw.Write(data)
		return
	}
	if t.dc != nil && t.dc.ReadyState() == webrtc.DataChannelStateOpen {
		t.sendBytes.Add(uint64(len(data)))
		_ = t.dc.Send(data)
	}
}

func (t *DCTunnel) SendData(data []byte) {
	DecodeFrames(data, func(connID uint32, msgType byte, payload []byte) {
		buf := make([]byte, 5+len(payload))
		binary.BigEndian.PutUint32(buf[0:4], connID)
		buf[4] = msgType
		copy(buf[5:], payload)
		wire := buf
		if t.obf != nil {
			wire = t.obf.EncryptPayload(buf)
			if wire == nil {
				return
			}
		}
		t.sendRaw(wire)
	})
}

func (t *DCTunnel) SetOnData(fn func([]byte)) { t.onData = fn }
func (t *DCTunnel) SetOnClose(fn func())      { t.onClose = fn }
func (t *DCTunnel) Reconfigure(fps, batch int) {}
