# vk-vpn benchmark notes template
# Run: cd c:\VPN\vk-vpn; .\scripts\bench-notes.ps1

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
$OutDir = Join-Path $repoRoot "bench-results"
$null = New-Item -ItemType Directory -Force -Path $OutDir

$stamp = Get-Date -Format "yyyy-MM-dd_HHmm"
$notesFile = Join-Path $OutDir "bench_$stamp.txt"

$template = @"
=== vk-vpn benchmark notes ===
Date: $(Get-Date -Format 'yyyy-MM-dd HH:mm')
Build/commit: _______________  (e.g. d0bd9ab = phase 1)
Network: [ ] Wi-Fi  [ ] Ethernet  Location: _______

--- Speedtest (do not swap) ---
Without VPN: yandex.ru/internet
With VPN:    speedtest.net (Ookla)

========== RUN A - baseline (same as phase 0) ==========
Same env as before phase 1. Only code changed (burst drain, queue, MTU env).

Env:
  VK_VPN_TUNNEL_MODE=video
  VK_VPN_VP8_FPS=24
  VK_VPN_VP8_BATCH=30
  VK_VPN_LOG_LEVEL=info
  (do not set VK_VPN_MTU = default 1400)

1. Without VPN - DL: _____ UL: _____ ping: _____
2. With VPN (Ookla) - DL: _____ (peak _____) UL: _____ ping: _____
   ICE joiner: _____
   ICE creator: _____
   ICE stable / disconnect: _____ / _____
3. curl via VPN (mirror.yandex.ru netboot tar.gz):
   bytes: _____ time: _____ Mbps: _____
   curl error (18?): _____

========== RUN B - optional aggressive pacing ==========
After RUN A. Add to env before start:

  VK_VPN_VP8_PROFILE=aggressive
  (30 fps x 50 batch if FPS/BATCH not set)

Repeat Ookla + curl only. DL: _____ UL: _____ curl OK? _____

========== RUN C - optional MTU ==========
  VK_VPN_MTU=1500
  (+ same env as A or B)

Repeat Ookla + curl. DL: _____ curl OK? _____

--- Conclusion ---
[ ] A better than phase 0 (higher DL / curl completes)
[ ] B better than A
[ ] No change - phase 2/3
[ ] sendQueue full in logs - try VK_VPN_VP8_SEND_QUEUE=2048

"@

$utf8 = New-Object System.Text.UTF8Encoding $true
[System.IO.File]::WriteAllText($notesFile, $template, $utf8)
Write-Host "Created: $notesFile"
Write-Host "Phase 1: RUN A first (same env), then B/C optional."
