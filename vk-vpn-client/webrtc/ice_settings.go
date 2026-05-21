package webrtc

import (
	"os"
	"strings"
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

// ICETransportPolicyFromEnv selects ICE candidate policy.
//
// Testing default (until release): relay — TURN only, matches whitelist/campus path.
// Prod / pion auto: set VK_VPN_ICE_TRANSPORT_POLICY=all (or auto|pion).
func ICETransportPolicyFromEnv() pion.ICETransportPolicy {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VK_VPN_ICE_TRANSPORT_POLICY"))) {
	case "all", "auto", "pion":
		logx.L("ice", "ICETransportPolicy=all (pion auto: host/srflx/prflx/relay)")
		return pion.ICETransportPolicyAll
	default:
		logx.L("ice", "ICETransportPolicy=relay (TURN only — testing default; release: VK_VPN_ICE_TRANSPORT_POLICY=all)")
		return pion.ICETransportPolicyRelay
	}
}

// ApplyICEPerformanceSettings tunes pion ICE. With relay-only policy, pion has no direct
// pairs to prefer — RelayAcceptanceMinWait is skipped.
func ApplyICEPerformanceSettings(se *pion.SettingEngine, policy pion.ICETransportPolicy) {
	if policy == pion.ICETransportPolicyRelay {
		logx.L("ice", "RelayAcceptanceMinWait skipped (ICETransportPolicy=relay)")
		return
	}
	wait := RelayAcceptanceMinWait()
	se.SetRelayAcceptanceMinWait(wait)
	logx.L("ice", "RelayAcceptanceMinWait=%s (pion may pick host/srflx before relay)", wait)
}
