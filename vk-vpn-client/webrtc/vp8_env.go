package webrtc

import (
	"os"
	"strconv"
	"strings"
)

// VP8PacingFromEnv reads VK_VPN_VP8_FPS, VK_VPN_VP8_BATCH, VK_VPN_VP8_PROFILE (defaults 24×30).
func VP8PacingFromEnv() (fps, batch int) {
	fps, batch = 24, 30
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("VK_VPN_VP8_PROFILE")))
	if profile == "aggressive" {
		fps, batch = 30, 50
	}
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
