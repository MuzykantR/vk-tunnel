package tunnel

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	DefaultVP8FPS       = 24
	DefaultVP8Batch     = 30
	keepaliveIdlePeriod = 100 * time.Millisecond
	vp8LargeSampleBytes = 4096
)

// VP8DataTunnel sends framed SOCKS data over a VP8 video track (whitelist-bypass).
type VP8DataTunnel struct {
	track        *webrtc.TrackLocalStaticSample
	logFn        func(string, ...interface{})
	obf          *TunnelObfuscator
	stopCh       chan struct{}
	sendQueue    chan []byte
	cfgChan      chan struct{}
	stopOnce     sync.Once
	running      atomic.Bool
	cfgMu        sync.Mutex
	fps          int
	batch        int
	sentFrames   atomic.Uint64
	recvFrames   atomic.Uint64
	OnData       func([]byte)
	OnClose      func()
}

func NewVP8DataTunnel(track *webrtc.TrackLocalStaticSample, obf *TunnelObfuscator, logFn func(string, ...interface{})) *VP8DataTunnel {
	depth := VP8SendQueueDepthFromEnv()
	return &VP8DataTunnel{
		track:     track,
		obf:       obf,
		logFn:     logFn,
		stopCh:    make(chan struct{}),
		sendQueue: make(chan []byte, depth),
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
	newFPS, newBatch := t.fps, t.batch
	t.cfgMu.Unlock()
	if !changed {
		return
	}
	if t.logFn != nil {
		t.logFn("vp8: reconfigure fps=%d batch=%d", newFPS, newBatch)
	}
	select {
	case t.cfgChan <- struct{}{}:
	default:
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
	payload := make([]byte, len(data))
	copy(payload, data)
	select {
	case t.sendQueue <- payload:
	case <-t.stopCh:
	}
}

func (t *VP8DataTunnel) SetOnData(fn func([]byte)) { t.OnData = fn }
func (t *VP8DataTunnel) SetOnClose(fn func())      { t.OnClose = fn }

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

func (t *VP8DataTunnel) currentIntervals() (sampleInterval time.Duration, keepaliveEvery, fps, batch int) {
	t.cfgMu.Lock()
	fps = t.fps
	batch = t.batch
	t.cfgMu.Unlock()

	frameInterval := time.Second / time.Duration(fps)
	sampleInterval = frameInterval
	if batch > 1 {
		sampleInterval = frameInterval / time.Duration(batch)
	}
	if sampleInterval <= 0 {
		sampleInterval = time.Millisecond
	}
	keepaliveEvery = int(keepaliveIdlePeriod / sampleInterval)
	if keepaliveEvery < 1 {
		keepaliveEvery = 1
	}
	return
}

func (t *VP8DataTunnel) encodeSample(data []byte) []byte {
	if t.obf != nil {
		return t.obf.EncodeData(data)
	}
	return data
}

func (t *VP8DataTunnel) logSent(n uint64, size int) {
	if t.logFn == nil {
		return
	}
	if size >= vp8LargeSampleBytes || n <= 5 || n%500 == 0 {
		t.logFn("vp8: sent #%d %d bytes", n, size)
	}
}

func (t *VP8DataTunnel) logRecv(n uint64, size int) {
	if t.logFn == nil {
		return
	}
	if size >= vp8LargeSampleBytes || n <= 5 || n%500 == 0 {
		t.logFn("vp8: recv #%d %d bytes", n, size)
	}
}

func (t *VP8DataTunnel) writeSample(sample []byte, dur time.Duration) {
	if len(sample) == 0 {
		return
	}
	if err := t.track.WriteSample(media.Sample{Data: sample, Duration: dur}); err != nil {
		if t.logFn != nil {
			t.logFn("vp8: WriteSample: %v", err)
		}
		return
	}
	n := t.sentFrames.Add(1)
	t.logSent(n, len(sample))
}

func (t *VP8DataTunnel) writerLoop() {
	for {
		sampleInterval, keepaliveEvery, fps, batch := t.currentIntervals()
		if t.logFn != nil {
			t.logFn("vp8: writer fps=%d batch=%d interval=%s keepaliveEvery=%d queueCap=%d",
				fps, batch, sampleInterval, keepaliveEvery, cap(t.sendQueue))
		}

		ticker := time.NewTicker(sampleInterval)
		idleTicks := 0
		reconfigure := false

		for !reconfigure {
			select {
			case <-t.stopCh:
				ticker.Stop()
				return
			case <-t.cfgChan:
				reconfigure = true
			case <-ticker.C:
				sentThisTick := 0
				maxBurst := batch
				if maxBurst < 1 {
					maxBurst = 1
				}
				pending := len(t.sendQueue)
				if pending > batch*2 {
					maxBurst = batch * 8
					if maxBurst > pending {
						maxBurst = pending
					}
					if maxBurst > 512 {
						maxBurst = 512
					}
					if t.logFn != nil && pending > batch*4 {
						t.logFn("vp8: backlog drain pending=%d burst=%d", pending, maxBurst)
					}
				}
				for sentThisTick < maxBurst {
					var sample []byte
					select {
					case data := <-t.sendQueue:
						sample = t.encodeSample(data)
						sentThisTick++
						idleTicks = 0
					default:
						goto afterBurst
					}
					t.writeSample(sample, sampleInterval)
				}
			afterBurst:
				if sentThisTick > 0 {
					continue
				}
				idleTicks++
				if idleTicks < keepaliveEvery {
					continue
				}
				idleTicks = 0
				if t.obf != nil {
					t.writeSample(t.obf.EncodeKeepalive(), sampleInterval)
				}
			}
		}
		ticker.Stop()
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
	if !res.HasFrame || res.SelfEcho {
		return
	}
	if res.PeerRestart && t.logFn != nil {
		t.logFn("vp8: peer restart epoch=0x%08x", res.PeerEpoch)
	}
	if res.Keepalive || len(res.Payload) == 0 {
		return
	}
	n := t.recvFrames.Add(1)
	t.logRecv(n, len(res.Payload))
	if t.OnData != nil {
		t.OnData(res.Payload)
	}
}
