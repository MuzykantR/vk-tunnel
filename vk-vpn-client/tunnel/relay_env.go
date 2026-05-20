package tunnel

import (
	"os"
	"strconv"
	"time"
)

const (
	defaultRelayConnectLimit = 0 // 0 = unlimited
	defaultRelayDrainTimeout = 120 * time.Second
)

// RelayConnectLimitFromEnv limits parallel creator-side TCP relays (VK_VPN_RELAY_CONNECT_LIMIT).
// 0 disables the limit. Default 0; use 32–48 to reduce ACK starvation during bulk download + browser.
func RelayConnectLimitFromEnv() int {
	if v := os.Getenv("VK_VPN_RELAY_CONNECT_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultRelayConnectLimit
}

// RelayDrainTimeoutFromEnv is how long to wait for VP8 sendQueue drain after origin EOF (VK_VPN_RELAY_DRAIN_TIMEOUT).
func RelayDrainTimeoutFromEnv() time.Duration {
	if v := os.Getenv("VK_VPN_RELAY_DRAIN_TIMEOUT"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	return defaultRelayDrainTimeout
}
