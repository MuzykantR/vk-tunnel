package logx

import (
	"sync/atomic"
)

var (
	dcActive   atomic.Int32
	dcOpened   atomic.Uint64
	dcClosed   atomic.Uint64
)

// DCOpen logs a new outbound TCP relay (debug only — avoids spam on page load).
func DCOpen(id uint32, addr string) {
	dcActive.Add(1)
	dcOpened.Add(1)
	Debug("dc", "open id=%d -> %s active=%d", id, addr, dcActive.Load())
}

func DCClose(id uint32, addr string, tx, rx int64) {
	dcActive.Add(-1)
	dcClosed.Add(1)
	L("dc", "close id=%d %s tx=%d rx=%d active=%d", id, addr, tx, rx, dcActive.Load())
}

func DCConnectFail(id uint32, addr string, err error) {
	Warn("dc", "dial id=%d %s: %v", id, addr, err)
}

// ResetDCStats clears the global relay counter (e.g. new TunnelSession after rejoin).
func ResetDCStats() {
	dcActive.Store(0)
}
