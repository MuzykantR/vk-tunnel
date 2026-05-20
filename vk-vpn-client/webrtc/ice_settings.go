package webrtc

import (
	"os"
	"time"

	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
)

const (
	defaultRelayAcceptanceWait     = 8 * time.Second
	defaultPreferDirectWaitRedirect = 3 * time.Second
)

// PreferDirectWaitBeforeRedirect is how long we wait for host/srflx before redirect on a relay pair (VK_VPN_ICE_PREFER_DIRECT_WAIT).
// Keep short (3s): whitelist users need redirect quickly; pion may still upgrade ICE later.
func PreferDirectWaitBeforeRedirect() time.Duration {
	if v := os.Getenv("VK_VPN_ICE_PREFER_DIRECT_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultPreferDirectWaitRedirect
}

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
