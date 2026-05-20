package tunnel

import (
	"os"
	"strconv"
)

const (
	defaultVP8SendQueue = 1024
	maxVP8SendQueue     = 8192
)

// VP8SendQueueDepthFromEnv returns sendQueue capacity (VK_VPN_VP8_SEND_QUEUE, default 1024).
func VP8SendQueueDepthFromEnv() int {
	if v := os.Getenv("VK_VPN_VP8_SEND_QUEUE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 256 {
			if n > maxVP8SendQueue {
				return maxVP8SendQueue
			}
			return n
		}
	}
	return defaultVP8SendQueue
}
