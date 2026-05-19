package tunnel

import (
	"sync/atomic"
	"time"

	"github.com/vk-vpn/client/logx"
)

// WatchdogOpts configures tunnel liveness probes over the framed protocol.
type WatchdogOpts struct {
	Interval    time.Duration
	MaxMiss     int
	OnUnhealthy func()
}

// TunnelWatchdog sends MsgPing on ControlConnID and expects MsgPong (P2).
type TunnelWatchdog struct {
	sendPing func()
	miss     atomic.Int32
	stopCh   chan struct{}
	opts     WatchdogOpts
}

func NewTunnelWatchdog(sendPing func(), opts WatchdogOpts) *TunnelWatchdog {
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Second
	}
	if opts.MaxMiss <= 0 {
		opts.MaxMiss = 3
	}
	return &TunnelWatchdog{
		sendPing: sendPing,
		stopCh:   make(chan struct{}),
		opts:     opts,
	}
}

func (w *TunnelWatchdog) NotifyPong() {
	w.miss.Store(0)
}

func (w *TunnelWatchdog) Start() {
	go w.loop()
}

func (w *TunnelWatchdog) Stop() {
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
}

func (w *TunnelWatchdog) loop() {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.sendPing()
			n := w.miss.Add(1)
			if int(n) >= w.opts.MaxMiss {
				logx.Warn("tunnel", "watchdog: %d missed pongs", n)
				w.miss.Store(0)
				if w.opts.OnUnhealthy != nil {
					w.opts.OnUnhealthy()
				}
			}
		}
	}
}
