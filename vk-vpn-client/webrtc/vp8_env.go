package webrtc

import (
	"os"
	"strconv"
)

// VP8PacingFromEnv reads VK_VPN_VP8_FPS and VK_VPN_VP8_BATCH (whitelist-bypass defaults 24×30).
func VP8PacingFromEnv() (fps, batch int) {
	fps, batch = 24, 30
	if v := os.Getenv("VK_VPN_VP8_FPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fps = n
		}
	}
	if v := os.Getenv("VK_VPN_VP8_BATCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batch = n
		}
	}
	return fps, batch
}
