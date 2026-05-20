# vk-vpn — шаблон заметок фазы 0 (бенчмарки)
# Запуск: .\scripts\bench-notes.ps1
# Заполняйте поля после каждого прогона; не коммитьте секреты.

$ErrorActionPreference = "Stop"
# Windows PowerShell 5.1: Join-Path accepts only two path segments
$repoRoot = Split-Path -Parent $PSScriptRoot
$OutDir = Join-Path $repoRoot "bench-results"
$null = New-Item -ItemType Directory -Force -Path $OutDir

$stamp = Get-Date -Format "yyyy-MM-dd_HHmm"
$notesFile = Join-Path $OutDir "bench_$stamp.txt"

$template = @"
=== vk-vpn benchmark notes (phase 0) ===
Date: $(Get-Date -Format "yyyy-MM-dd HH:mm")
Git/commit or deploy tag: _______________
Network: [ ] Wi-Fi  [ ] Ethernet  Location: home / campus

--- Env (baseline phase 0) ---
VK_VPN_TUNNEL_MODE=video
VK_VPN_VP8_FPS=24
VK_VPN_VP8_BATCH=30
VK_VPN_LOG_LEVEL=info

--- 1. Without VPN (yandex.ru/internet) ---
DL Mbps: _______
UL Mbps: _______
Ping ms: _______
Speedtest result ID: _______

--- 2. With VPN (speedtest.net / Ookla) ---
DL Mbps: _______
UL Mbps: _______
Ping ms: _______
Speedtest result ID: _______
ICE selected (paste log line):
  joiner: 
  creator: 
ICE stable 60s under load: [ ] yes  [ ] no
Disconnect/restart during test: [ ] yes  [ ] no

--- Speedtest (do not swap) ---
Without VPN: yandex.ru/internet  Country: _______
With VPN:    speedtest.net (Ookla)  Country: _______

--- 3. Download-only (curl, VPN on) ---
URL used: _______
size_download bytes: _______
time_total s: _______
Computed Mbps: _______
curl error (e.g. 18 partial): _______

--- Conclusion (pick one) ---
[ ] Home uplink ~20 Mbps limits VPN (~18 expected)
[ ] Line is fast; tunnel is the bottleneck -> phase 1
[ ] ICE unstable -> fix stability before throughput

"@

Set-Content -Path $notesFile -Value $template -Encoding UTF8
Write-Host "Created: $notesFile"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Run tests per docs/BENCHMARK_PROTOCOL.md"
Write-Host "  2. Fill in the file above"
Write-Host "  3. Copy numbers to docs/THROUGHPUT_HYPOTHESES.md section 9"
Write-Host "  4. Tell the agent: phase 0 done -> start phase 1"
