package webrtc

import (
	"os"
	"time"

	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
)

const defaultRelayAcceptanceWait = 12 * time.Second

// RelayAcceptanceMinWait returns how long pion delays picking a TURN relay pair (VK_VPN_ICE_RELAY_WAIT).
func RelayAcceptanceMinWait() time.Duration {
	if v := os.Getenv("VK_VPN_ICE_RELAY_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultRelayAcceptanceWait
}

// ApplyICEPerformanceSettings tunes pion ICE for throughput (WLB-style: prefer direct/srflx over relay).
func ApplyICEPerformanceSettings(se *pion.SettingEngine) {
	wait := RelayAcceptanceMinWait()
	se.SetRelayAcceptanceMinWait(wait)
	logx.L("ice", "RelayAcceptanceMinWait=%s (direct host/srflx preferred before relay)", wait)
}
