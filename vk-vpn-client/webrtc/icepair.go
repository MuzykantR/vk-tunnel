package webrtc

import (
	"fmt"
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

type icePairView struct {
	loc       pion.ICECandidateStats
	rem       pion.ICECandidateStats
	nominated bool
}

// selectedICECandidatePair picks the nominated succeeded pair with both IPs known.
// GetStats() order is undefined — candidate stats must be collected before pairs.
func selectedICECandidatePair(pc *pion.PeerConnection) (icePairView, bool) {
	if pc == nil {
		return icePairView{}, false
	}
	cands := make(map[string]pion.ICECandidateStats)
	for _, stat := range pc.GetStats() {
		if s, ok := stat.(pion.ICECandidateStats); ok {
			cands[s.ID] = s
		}
	}
	var fallback *icePairView
	for _, stat := range pc.GetStats() {
		pair, ok := stat.(pion.ICECandidatePairStats)
		if !ok || pair.State != pion.StatsICECandidatePairStateSucceeded {
			continue
		}
		loc, lok := cands[pair.LocalCandidateID]
		rem, rok := cands[pair.RemoteCandidateID]
		if !lok || !rok || loc.IP == "" || rem.IP == "" {
			continue
		}
		view := icePairView{loc: loc, rem: rem, nominated: pair.Nominated}
		if pair.Nominated {
			return view, true
		}
		if fallback == nil {
			fallback = &view
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return icePairView{}, false
}

// LogSelectedICEPair logs local/remote candidate types via GetStats (P1, pion v3).
// Returns true if either side uses relay (TURN).
func LogSelectedICEPair(pc *pion.PeerConnection, tag string) bool {
	view, ok := selectedICECandidatePair(pc)
	if !ok {
		logx.Warn(tag, "ICE pair: no succeeded pair with IPs in stats yet")
		return false
	}
	locT := shortType(view.loc.CandidateType)
	remT := shortType(view.rem.CandidateType)
	nom := ""
	if view.nominated {
		nom = " nominated"
	}
	logx.L(tag, "ICE selected%s %s/%s:%d <-> %s/%s:%d (%s -> %s)",
		nom, locT, view.loc.Protocol, view.loc.Port, remT, view.rem.Protocol, view.rem.Port,
		view.loc.IP, view.rem.IP)
	relay := locT == "relay" || remT == "relay"
	if relay {
		side := "remote"
		if locT == "relay" {
			side = "local"
		}
		logx.Warn(tag, "ICE uses TURN relay (%s side) %s -> %s; slower than host/srflx — increase VK_VPN_ICE_RELAY_WAIT or check STUN/UDP bypass",
			side, view.loc.IP, view.rem.IP)
	} else {
		logx.L(tag, "ICE direct path (no TURN relay in nominated pair)")
	}
	return relay
}

// ICEBypassIPs returns public IPv4 of the nominated succeeded ICE pair only.
func ICEBypassIPs(pc *pion.PeerConnection) []string {
	loc, rem, ok := SelectedICEPairIPs(pc)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	var ips []string
	for _, ip := range []string{loc, rem} {
		p := net.ParseIP(ip)
		if p == nil || p.To4() == nil {
			continue
		}
		if p.IsLoopback() || p.IsLinkLocalUnicast() || p.IsPrivate() {
			continue
		}
		if _, dup := seen[ip]; dup {
			continue
		}
		seen[ip] = struct{}{}
		ips = append(ips, ip)
	}
	return ips
}

// SelectedICEPairUsesRelay reports whether the nominated pair includes a TURN relay candidate.
func SelectedICEPairUsesRelay(pc *pion.PeerConnection) bool {
	view, ok := selectedICECandidatePair(pc)
	if !ok {
		return false
	}
	locT := shortType(view.loc.CandidateType)
	remT := shortType(view.rem.CandidateType)
	return locT == "relay" || remT == "relay"
}

// LogGatheredICECandidates logs candidate types from GetStats (diagnostics).
func LogGatheredICECandidates(pc *pion.PeerConnection, tag string) {
	if pc == nil {
		return
	}
	seen := make(map[string]struct{})
	var entries []string
	for _, stat := range pc.GetStats() {
		c, ok := stat.(pion.ICECandidateStats)
		if !ok || c.IP == "" {
			continue
		}
		key := fmt.Sprintf("%s|%s", c.IP, shortType(c.CandidateType))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		turn := ""
		if c.URL != "" {
			turn = " turn=" + c.URL
		}
		entries = append(entries, fmt.Sprintf("%s/%s:%d@%s%s", shortType(c.CandidateType), c.Protocol, c.Port, c.IP, turn))
	}
	if len(entries) == 0 {
		logx.Debug(tag, "ICE candidate stats empty (may appear after nomination)")
		return
	}
	logx.L(tag, "ICE candidates in stats: %s", strings.Join(entries, " "))
}

// SelectedICEPairIPs returns local and remote IPv4 of the nominated succeeded pair.
func SelectedICEPairIPs(pc *pion.PeerConnection) (localIP, remoteIP string, ok bool) {
	view, ok := selectedICECandidatePair(pc)
	if !ok {
		return "", "", false
	}
	return view.loc.IP, view.rem.IP, true
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
