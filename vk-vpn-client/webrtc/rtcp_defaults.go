// RTCP feedback restoration for Test A.
//
// Background: pion's NewAPI() without WithInterceptorRegistry creates
// an empty interceptor chain — no NACK responder, no TWCC reporter,
// no RTCP sender/receiver reports. Standard WebRTC senders (including
// the VK SFU we tunnel through) rely on these RTCP signals to estimate
// receiver capacity (timing-based BWE via TWCC, loss-based via NACK)
// and decide per-track bandwidth budget.
//
// In their absence, the SFU sees a "silent" receiver and defaults to a
// conservative mobile profile, never upgrading because it gets no
// positive signal that the other side can take more. The observed
// ~1.3 Mbps sustained cap (with token-bucket-shaped bursts to ~2.8
// Mbps on cellular) matches the shape of a default mobile bitrate
// budget governed by a server-side rate shaper.
//
// Test A hypothesis: registering the default interceptor chain on
// both peers will produce the RTCP feedback the SFU needs to upgrade
// the budget, lifting the cap without any media-mimicry work. If
// confirmed, the more expensive bitstream-validation experiments
// (Test B = pseudo-valid VP8 prefix, Test C = full synthetic frame)
// become unnecessary.

package webrtc

import (
	"log"

	"github.com/pion/interceptor"
	pion "github.com/pion/webrtc/v3"
)

// InstallRTCPDefaults builds an interceptor.Registry populated with
// pion's default chain and returns it. The caller passes the registry
// to webrtc.NewAPI via webrtc.WithInterceptorRegistry.
//
// On the diag/ru-vps-test branch this is unconditional — the
// VK_VPN_RTCP_DEFAULTS=0 env-gate that lives on main is intentionally
// absent so the test runs against the production baseline.
//
// The chain registered (per pion v3 RegisterDefaultInterceptors):
//
//   - nack.GeneratorInterceptor   — receiver-side, emits NACK RTCP on
//     detected packet loss in the inbound stream
//   - nack.ResponderInterceptor   — sender-side, retransmits packets
//     requested by incoming NACK
//   - twcc.HeaderExtensionInterceptor — sender-side, stamps each
//     outbound RTP with a transport-wide sequence number
//   - twcc.SenderInterceptor      — receiver-side, sends transport-wide
//     feedback RTCP carrying arrival times for the SFU's BWE
//   - report.SenderInterceptor    — emits standard RTCP Sender Reports
//   - report.ReceiverInterceptor  — emits standard RTCP Receiver Reports
//
// Notably this does NOT include the GCC pacer. We do not want pion's
// own congestion control fighting whatever the SFU decides — we just
// want the SFU to receive the standard feedback it expects.
func InstallRTCPDefaults(me *pion.MediaEngine, who string) *interceptor.Registry {
	ir := &interceptor.Registry{}
	if err := pion.RegisterDefaultInterceptors(me, ir); err != nil {
		log.Printf("[%s] rtcp-defaults: RegisterDefaultInterceptors err: %v — falling back to empty registry", who, err)
		return &interceptor.Registry{}
	}
	log.Printf("[%s] rtcp-defaults: installed (NACK + TWCC + RR/SR)", who)
	return ir
}
