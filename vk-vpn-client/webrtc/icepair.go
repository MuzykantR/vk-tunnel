package webrtc

import (
	"strings"
	"time"

	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
)

// ScheduleICEPairLogging logs the selected pair now and again after 30s (nomination may change).
func ScheduleICEPairLogging(pc *pion.PeerConnection, tag string) {
	go LogSelectedICEPair(pc, tag)
	time.AfterFunc(30*time.Second, func() {
		if pc == nil || pc.ConnectionState() == pion.PeerConnectionStateClosed {
			return
		}
		LogSelectedICEPair(pc, tag)
	})
}

// LogSelectedICEPair logs local/remote candidate types via GetStats (P1, pion v3).
// Returns true if either side uses relay (TURN).
func LogSelectedICEPair(pc *pion.PeerConnection, tag string) bool {
	if pc == nil {
		return false
	}
	cands := make(map[string]pion.ICECandidateStats)
	for _, stat := range pc.GetStats() {
		switch s := stat.(type) {
		case pion.ICECandidateStats:
			cands[s.ID] = s
		case pion.ICECandidatePairStats:
			if s.State != pion.StatsICECandidatePairStateSucceeded {
				continue
			}
			loc := cands[s.LocalCandidateID]
			rem := cands[s.RemoteCandidateID]
			locT := shortType(loc.CandidateType)
			remT := shortType(rem.CandidateType)
			logx.L(tag, "ICE selected %s/%s <-> %s/%s (%s -> %s)",
				locT, loc.Protocol, remT, rem.Protocol, loc.IP, rem.IP)
			relay := locT == "relay" || remT == "relay"
			if relay {
				logx.Warn(tag, "ICE path uses relay (TURN) — try longer VK_VPN_ICE_RELAY_WAIT or check bypass routes for direct/srflx")
			}
			return relay
		}
	}
	logx.Warn(tag, "ICE pair: no succeeded pair in stats yet")
	return false
}

func shortType(t pion.ICECandidateType) string {
	s := strings.ToLower(t.String())
	switch {
	case strings.Contains(s, "relay"):
		return "relay"
	case strings.Contains(s, "srflx"):
		return "srflx"
	case strings.Contains(s, "prflx"):
		return "prflx"
	default:
		return "host"
	}
}
