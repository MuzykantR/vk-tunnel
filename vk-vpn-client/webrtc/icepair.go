package webrtc

import (
	"strings"

	pion "github.com/pion/webrtc/v3"
	"github.com/vk-vpn/client/logx"
)

// LogSelectedICEPair logs local/remote candidate types via GetStats (P1, pion v3).
func LogSelectedICEPair(pc *pion.PeerConnection, tag string) {
	if pc == nil {
		return
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
			logx.L(tag, "ICE selected %s/%s <-> %s/%s (%s -> %s)",
				shortType(loc.CandidateType), loc.Protocol,
				shortType(rem.CandidateType), rem.Protocol,
				loc.IP, rem.IP)
			return
		}
	}
	logx.Warn(tag, "ICE pair: no succeeded pair in stats yet")
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
