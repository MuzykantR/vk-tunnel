package webrtc

import (
	"os"
	"time"

	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
)

const defaultRelayAcceptanceWait = 3 * time.Second

// ApplyICEPerformanceSettings tunes pion ICE for throughput (WLB-style: prefer direct/srflx over relay).
func ApplyICEPerformanceSettings(se *pion.SettingEngine) {
	wait := defaultRelayAcceptanceWait
	if v := os.Getenv("VK_VPN_ICE_RELAY_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			wait = d
		}
	}
	se.SetRelayAcceptanceMinWait(wait)
	logx.Debug("ice", "RelayAcceptanceMinWait=%s", wait)
}
