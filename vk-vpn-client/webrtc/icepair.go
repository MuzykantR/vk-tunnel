package webrtc

import (
	"net"
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

// ICEBypassIPs collects every public IPv4 from ICE stats (candidates + succeeded pairs).
func ICEBypassIPs(pc *pion.PeerConnection) []string {
	if pc == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var ips []string
	add := func(ip string) {
		p := net.ParseIP(ip)
		if p == nil || p.To4() == nil {
			return
		}
		if p.IsLoopback() || p.IsLinkLocalUnicast() || p.IsPrivate() {
			return
		}
		if _, ok := seen[ip]; ok {
			return
		}
		seen[ip] = struct{}{}
		ips = append(ips, ip)
	}
	cands := make(map[string]pion.ICECandidateStats)
	for _, stat := range pc.GetStats() {
		switch s := stat.(type) {
		case pion.ICECandidateStats:
			cands[s.ID] = s
			add(s.IP)
		case pion.ICECandidatePairStats:
			if s.State != pion.StatsICECandidatePairStateSucceeded {
				continue
			}
			if loc, ok := cands[s.LocalCandidateID]; ok {
				add(loc.IP)
			}
			if rem, ok := cands[s.RemoteCandidateID]; ok {
				add(rem.IP)
			}
		}
	}
	return ips
}

// SelectedICEPairIPs returns local and remote IPv4 of the nominated succeeded pair.
func SelectedICEPairIPs(pc *pion.PeerConnection) (localIP, remoteIP string, ok bool) {
	if pc == nil {
		return "", "", false
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
			if loc.IP != "" && rem.IP != "" {
				return loc.IP, rem.IP, true
			}
		}
	}
	return "", "", false
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
