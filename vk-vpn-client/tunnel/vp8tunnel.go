package tunnel

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	DefaultVP8FPS   = 24
	DefaultVP8Batch = 30
	vp8SendQueue    = 1024
)

// VP8DataTunnel sends framed SOCKS data over a VP8 video track (whitelist-bypass).
type VP8DataTunnel struct {
	track     *webrtc.TrackLocalStaticSample
	logFn     func(string, ...interface{})
	obf       *TunnelObfuscator
	stopCh    chan struct{}
	sendQueue chan []byte
	cfgChan   chan struct{}
	stopOnce  sync.Once
	running   atomic.Bool
	cfgMu     sync.Mutex
	fps       int
	batch     int
	OnData    func([]byte)
	OnClose   func()
}

func NewVP8DataTunnel(track *webrtc.TrackLocalStaticSample, obf *TunnelObfuscator, logFn func(string, ...interface{})) *VP8DataTunnel {
	return &VP8DataTunnel{
		track:     track,
		obf:       obf,
		logFn:     logFn,
		stopCh:    make(chan struct{}),
		sendQueue: make(chan []byte, vp8SendQueue),
		cfgChan:   make(chan struct{}, 1),
		fps:       DefaultVP8FPS,
		batch:     DefaultVP8Batch,
	}
}

func (t *VP8DataTunnel) Reconfigure(fps, batch int) {
	if fps <= 0 && batch <= 0 {
		return
	}
	t.cfgMu.Lock()
	changed := false
	if fps > 0 && t.fps != fps {
		t.fps = fps
		changed = true
	}
	if batch > 0 && t.batch != batch {
		t.batch = batch
		changed = true
	}
	t.cfgMu.Unlock()
	if changed {
		select {
		case t.cfgChan <- struct{}{}:
		default:
		}
	}
}

func (t *VP8DataTunnel) FPS() int {
	t.cfgMu.Lock()
	defer t.cfgMu.Unlock()
	return t.fps
}

func (t *VP8DataTunnel) Batch() int {
	t.cfgMu.Lock()
	defer t.cfgMu.Unlock()
	return t.batch
}

func (t *VP8DataTunnel) SendData(data []byte) {
	if len(data) == 0 {
		return
	}
	select {
	case t.sendQueue <- data:
	case <-t.stopCh:
	}
}

func (t *VP8DataTunnel) SetOnData(fn func([]byte))  { t.OnData = fn }
func (t *VP8DataTunnel) SetOnClose(fn func())       { t.OnClose = fn }

func (t *VP8DataTunnel) Start(fps, batch int) {
	t.cfgMu.Lock()
	if fps > 0 {
		t.fps = fps
	}
	if batch > 0 {
		t.batch = batch
	}
	t.cfgMu.Unlock()
	if !t.running.CompareAndSwap(false, true) {
		return
	}
	go t.writerLoop()
}

func (t *VP8DataTunnel) Stop() {
	if !t.running.CompareAndSwap(true, false) {
		return
	}
	t.stopOnce.Do(func() { close(t.stopCh) })
	if t.OnClose != nil {
		t.OnClose()
	}
}

func (t *VP8DataTunnel) HandleFrame(frame []byte) {
	if t.obf == nil {
		if len(frame) > 0 && t.OnData != nil {
			t.OnData(frame)
		}
		return
	}
	res := t.obf.Decode(frame)
	if !res.HasFrame || res.SelfEcho || res.Keepalive || len(res.Payload) == 0 {
		return
	}
	if t.OnData != nil {
		t.OnData(res.Payload)
	}
}

func (t *VP8DataTunnel) writerLoop() {
	const keepaliveEvery = 10
	for {
		t.cfgMu.Lock()
		fps, batch := t.fps, t.batch
		t.cfgMu.Unlock()
		frameInterval := time.Second / time.Duration(fps)
		sampleInterval := frameInterval
		if batch > 1 {
			sampleInterval = frameInterval / time.Duration(batch)
		}
		if sampleInterval <= 0 {
			sampleInterval = time.Millisecond
		}
		ticker := time.NewTicker(sampleInterval)
		idle := 0
		reconfigure := false
		for !reconfigure {
			select {
			case <-t.stopCh:
				ticker.Stop()
				return
			case <-t.cfgChan:
				reconfigure = true
			case <-ticker.C:
				var sample []byte
				select {
				case data := <-t.sendQueue:
					if t.obf != nil {
						sample = t.obf.EncodeData(data)
					} else {
						sample = data
					}
					idle = 0
				default:
					idle++
					if idle < keepaliveEvery {
						continue
					}
					idle = 0
					if t.obf != nil {
						sample = t.obf.EncodeKeepalive()
					}
				}
				if len(sample) == 0 {
					continue
				}
				_ = t.track.WriteSample(media.Sample{Data: sample, Duration: sampleInterval})
			}
		}
		ticker.Stop()
	}
}
