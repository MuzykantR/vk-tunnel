# Один curl, один файл лога. Запусти после Connect:
#   .\scripts\bench-curl.ps1
# Скрипт ничего не настраивает — режим (video/dc) определяется тем, что
# уже запущено. Какой mode активен — видно в app.log по строкам
# "VP8 TUNNEL CONNECTED" (= video) или "VPN tunnel DataChannel open (mode=dc)".

$ErrorActionPreference = "Continue"

$url = "http://mirror.yandex.ru/ubuntu-releases/24.04/ubuntu-24.04.4-netboot-amd64.tar.gz"
$expectedSize = 96331062   # ~92 MB

Write-Host "curl $url"
$start = Get-Date
$out = & curl.exe -o NUL -w "%{size_download} %{time_total} %{http_code}" $url 2>&1 | Select-Object -Last 1
$elapsed = (Get-Date) - $start

$parts = $out -split '\s+'
if ($parts.Count -ge 3) {
    $bytes = [int64]$parts[0]
    $time = [double]$parts[1]
    $code = $parts[2]
    $mbps = if ($time -gt 0) { [math]::Round($bytes * 8 / $time / 1e6, 2) } else { 0 }
    $percent = [math]::Round($bytes * 100.0 / $expectedSize, 2)
    Write-Host ""
    Write-Host "=== Result ==="
    Write-Host "bytes:     $bytes / $expectedSize  ($percent%)"
    Write-Host "time:      ${time}s"
    Write-Host "Mbps:      $mbps"
    Write-Host "http_code: $code"
    Write-Host "wall:      $($elapsed.TotalSeconds)s"
} else {
    Write-Host "curl output unexpected: $out"
}
