# vk-vpn benchmark template — Phase 2: DC vs VP8 in relay-only mode
# Goal: decide between Hybrid-A (DC bulk + VP8 cover) and Hybrid-B (VP8 bulk + DC ACK)
# Run: cd C:\VPN\vk-vpn; .\scripts\bench-dc-vs-vp8-relay.ps1

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$OutDir = Join-Path $repoRoot "bench-results"
$null = New-Item -ItemType Directory -Force -Path $OutDir

$stamp = Get-Date -Format "yyyy-MM-dd_HHmm"
$notesFile = Join-Path $OutDir "bench_dc_vs_vp8_relay_$stamp.txt"

$template = @"
=== vk-vpn Phase 2 benchmark: DC vs VP8 in TURN-relay ===
Date: $(Get-Date -Format 'yyyy-MM-dd HH:mm')
Branch: feat/transport-rework
Commit: ________________________________________________
Network: [ ] home wifi  [ ] ethernet  [ ] other:______
VPS region: ________________________________________________
RTT (ms) to VPS without VPN: _______ (ping VPS IP)

=== Mandatory env (BOTH runs) ===
  VK_VPN_ICE_TRANSPORT_POLICY=relay
  VK_VPN_EXTRA_BYPASS_IPS=<VPS-IP>      # required for SSH during relay
  VK_VPN_LOG_LEVEL=info

=== Variable env ===
RUN-A (VP8 bulk, baseline):
  VK_VPN_TUNNEL_MODE=video
  VK_VPN_VP8_FPS=24
  VK_VPN_VP8_BATCH=30

RUN-B (DC bulk):
  VK_VPN_TUNNEL_MODE=dc

=== Test target (same file, both runs) ===
URL: http://mirror.yandex.ru/ubuntu-releases/24.04/ubuntu-24.04.4-netboot-amd64.tar.gz
Size: ~92 MB

curl invocation:
  curl.exe -o NUL -w "bytes=%%{size_download} time=%%{time_total}s http_code=%%{http_code}``n" "<URL>"

================= RUN-A — VP8 bulk in relay =================
[ ] Set env above
[ ] Start vk-vpn-server (VPS) — confirm logs say:
        creator] ICE Connected
        creator] === MODE: VIDEO (VP8) ===
[ ] Start vk-vpn-client (joiner) — confirm logs say:
        Client ICE state: connected
        ICE selected … relay/<protocol>:<port> <-> relay/…   ← MUST contain "relay"
        VP8 TUNNEL CONNECTED
[ ] Run curl 5 times, record below
[ ] Save server.log + client.log

| # | bytes_downloaded | time (s) | Mbps | size_OK? | http_code |
|---|------------------|----------|------|----------|-----------|
| 1 |                  |          |      |          |           |
| 2 |                  |          |      |          |           |
| 3 |                  |          |      |          |           |
| 4 |                  |          |      |          |           |
| 5 |                  |          |      |          |           |

  avg Mbps (5 runs): _______
  100% completions:  ___ / 5
  any conn reset?:   [ ] yes  [ ] no

From client log (vp8 reassembler stats — interval lines):
  total frames emitted: _______
  total gap_resets:     _______      ← non-zero means RTP loss → byte loss
  total depacket_errs:  _______
  gap_resets per 1000 frames: _______

================= RUN-B — DC bulk in relay =================
[ ] Restart client/server with VK_VPN_TUNNEL_MODE=dc
[ ] Confirm client log: "VPN tunnel DataChannel open (mode=dc)"
[ ] Confirm NO line "VP8 TUNNEL CONNECTED" appears (we are not in video mode)
[ ] Run curl 5 times, record below

| # | bytes_downloaded | time (s) | Mbps | size_OK? | http_code |
|---|------------------|----------|------|----------|-----------|
| 1 |                  |          |      |          |           |
| 2 |                  |          |      |          |           |
| 3 |                  |          |      |          |           |
| 4 |                  |          |      |          |           |
| 5 |                  |          |      |          |           |

  avg Mbps (5 runs): _______
  100% completions:  ___ / 5
  any conn reset?:   [ ] yes  [ ] no

================= Decision matrix =================
Hybrid-A (DC bulk + VP8 cover) is viable iff:
  - RUN-B avg Mbps >= 70% of RUN-A avg Mbps     [ ] yes  [ ] no
  - RUN-B 5/5 completions at 100%               [ ] yes  [ ] no

If both YES  → proceed to Hybrid-A (VP8 noise + DC bulk).
If RUN-B fails completions → confirms SCTP-CC throttled by relay losses;
                              proceed to Hybrid-B (KCP-over-VP8 + DC ACK).
If RUN-B passes completions but Mbps < 30% of A → toss-up. Discuss with team.

================= Observations =================
ICE pair RUN-A (from "ICE selected" log): _________________________________
ICE pair RUN-B (from "ICE selected" log): _________________________________
TURN server used: _______________________________________________________
Anything else interesting:

"@

$utf8 = New-Object System.Text.UTF8Encoding $true
[System.IO.File]::WriteAllText($notesFile, $template, $utf8)
Write-Host "Created: $notesFile"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Fill VPS IP / commit hash at the top."
Write-Host "  2. Run RUN-A (VP8 bulk) — 5 iterations of curl. Save logs."
Write-Host "  3. Run RUN-B (DC bulk)  — 5 iterations of curl. Save logs."
Write-Host "  4. Fill decision matrix. Share file back."
