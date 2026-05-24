package tunnel

import (
	"os"
	"strconv"
	"strings"
)

// KCPConfig is the full set of knobs we expose to env. Defaults are
// tuned for the vk-vpn use case: TURN-relay UDP-ish channel, ~100 ms
// RTT, real-world packet loss bursts up to ~30%.
//
// The "turbo" defaults (NoDelay=1, NoCongestion=1) are intentionally
// aggressive — KCP-internal congestion control is more conservative
// than what we need against a TURN policer. We let the FEC layer and
// retransmits absorb loss instead of throttling cwnd.
type KCPConfig struct {
	NoDelay      int // 0 = normal, 1 = enable nodelay/turbo
	Interval     int // ms — KCP internal tick
	Resend       int // ACK-skip count before fast retransmit (2 = standard turbo)
	NoCongestion int // 0 = use KCP CC, 1 = disable

	SendWindow int // packets
	RecvWindow int // packets
	MTU        int // bytes — must leave headroom for fake-VP8 header + obf nonce/tag + DTLS overhead

	DataShards   int // FEC: number of data shards per block; 0 disables FEC
	ParityShards int // FEC: number of parity shards per block; 0 disables FEC

	// LinearACK turns on ack-no-delay — every received packet generates
	// an immediate ACK, instead of waiting for the next tick. Lower
	// throughput overhead, faster recovery on losses.
	ACKNoDelay bool

	// WriteDelay=false flushes user writes immediately into KCP send
	// buffer. Default for us: false (low-latency).
	WriteDelay bool
}

func defaultKCPConfig() KCPConfig {
	return KCPConfig{
		NoDelay:      1,
		Interval:     10,
		Resend:       2,
		NoCongestion: 1,

		SendWindow: 2048,
		RecvWindow: 2048,
		MTU:        1200,

		DataShards:   10,
		ParityShards: 3,

		ACKNoDelay: true,
		WriteDelay: false,
	}
}

// KCPConfigFromEnv reads optional overrides:
//
//	VK_VPN_KCP_FEC      = "10:3" (default) or "off" / "0:0" to disable
//	VK_VPN_KCP_WINDOW   = "2048" (same value for send and recv)
//	VK_VPN_KCP_MTU      = "1200" (must be 400..1400)
//	VK_VPN_KCP_NODELAY  = "1,10,2,1" (nodelay,interval,resend,nc)
func KCPConfigFromEnv() KCPConfig {
	cfg := defaultKCPConfig()

	if v := strings.TrimSpace(os.Getenv("VK_VPN_KCP_FEC")); v != "" {
		switch strings.ToLower(v) {
		case "off", "0", "0:0", "none", "disabled":
			cfg.DataShards = 0
			cfg.ParityShards = 0
		default:
			if parts := strings.SplitN(v, ":", 2); len(parts) == 2 {
				if d, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && d > 0 {
					cfg.DataShards = d
				}
				if p, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && p >= 0 {
					cfg.ParityShards = p
				}
			}
		}
	}

	if v := os.Getenv("VK_VPN_KCP_WINDOW"); v != "" {
		if w, err := strconv.Atoi(v); err == nil && w >= 64 && w <= 32768 {
			cfg.SendWindow = w
			cfg.RecvWindow = w
		}
	}

	if v := os.Getenv("VK_VPN_KCP_MTU"); v != "" {
		if m, err := strconv.Atoi(v); err == nil && m >= 400 && m <= 1400 {
			cfg.MTU = m
		}
	}

	if v := os.Getenv("VK_VPN_KCP_NODELAY"); v != "" {
		parts := strings.Split(v, ",")
		ints := make([]int, 0, 4)
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				break
			}
			ints = append(ints, n)
		}
		if len(ints) == 4 {
			cfg.NoDelay, cfg.Interval, cfg.Resend, cfg.NoCongestion = ints[0], ints[1], ints[2], ints[3]
		}
	}

	return cfg
}
