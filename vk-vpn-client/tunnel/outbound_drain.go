package tunnel

import "time"

// OutboundQueued is implemented by tunnels with an internal send queue (VP8).
type OutboundQueued interface {
	SendQueueDepth() int
	WaitSendQueueDrained(timeout time.Duration) int
}

// WaitOutboundDrain blocks until the tunnel outbound queue is empty or timeout expires.
// Returns remaining queue depth (0 = fully drained).
func WaitOutboundDrain(t DataTunnel, timeout time.Duration) int {
	q, ok := t.(OutboundQueued)
	if !ok {
		return 0
	}
	if q.SendQueueDepth() == 0 {
		return 0
	}
	return q.WaitSendQueueDrained(timeout)
}
