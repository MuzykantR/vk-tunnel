<#
.SYNOPSIS
    Per-second throughput logger over a SOCKS5 proxy. Built for the
    diag/ru-vps-test branch but works against any HTTP(S) endpoint
    reachable via a local SOCKS5 listener.

.DESCRIPTION
    Spawns curl as a background process with HTTPS_PROXY pointed at
    127.0.0.1:<SocksPort>, samples the downloaded byte count every
    second, prints KB/s and total MB so far, and at the end emits a
    summary (mean / median / p95 / max-burst / duration).

    The token-bucket shape we care about (RefillRate vs BurstSize) is
    visible *only* with sub-second granularity — averaging over the
    whole transfer hides the burst-and-hold pattern that
    distinguishes server-side shaping from path congestion.

.PARAMETER Url
    URL to download. Default: hetzner 100 MB test file.

.PARAMETER SocksPort
    Local SOCKS5 port. Default: 1080.

.PARAMETER OutFile
    Optional path to write the raw per-second samples as CSV.

.EXAMPLE
    .\measure-throughput.ps1
    .\measure-throughput.ps1 -Url https://speed.hetzner.com/100MB.bin -SocksPort 1080
    .\measure-throughput.ps1 -OutFile run1.csv
#>

[CmdletBinding()]
param(
    [string] $Url = "https://speed.hetzner.com/100MB.bin",
    [int]    $SocksPort = 1080,
    [string] $OutFile = ""
)

$ErrorActionPreference = "Stop"

$proxy = "socks5h://127.0.0.1:$SocksPort"
$tmp = New-TemporaryFile

Write-Host ""
Write-Host "=== Per-second throughput logger ===" -ForegroundColor Cyan
Write-Host "URL:           $Url"
Write-Host "SOCKS5 proxy:  $proxy"
Write-Host "Temp dump:     $($tmp.FullName)"
Write-Host ""

$curlExe = (Get-Command curl.exe -ErrorAction SilentlyContinue).Source
if (-not $curlExe) {
    throw "curl.exe not found in PATH. Install curl or pin the full path."
}

# Spawn curl in the background; capture nothing — we only watch the
# growing file size on disk.
$startedAt = Get-Date
$proc = Start-Process -FilePath $curlExe -ArgumentList @(
    "--silent",
    "--show-error",
    "--proxy", $proxy,
    "--output", $tmp.FullName,
    $Url
) -PassThru -NoNewWindow -RedirectStandardError "$($tmp.FullName).stderr"

$samples = New-Object System.Collections.Generic.List[psobject]
$prevBytes = 0L
$tick = 0

Write-Host ("{0,3} | {1,10} | {2,10} | {3,10}" -f "s", "KB/s", "MB total", "% if 100MB") -ForegroundColor DarkGray
Write-Host ("-" * 50) -ForegroundColor DarkGray

while (-not $proc.HasExited) {
    Start-Sleep -Seconds 1
    $tick++

    $bytesNow = 0L
    if (Test-Path $tmp.FullName) {
        $bytesNow = (Get-Item $tmp.FullName).Length
    }
    $deltaBytes = $bytesNow - $prevBytes
    $prevBytes  = $bytesNow

    $kbps = [math]::Round($deltaBytes / 1024.0, 1)
    $mbTotal = [math]::Round($bytesNow / 1048576.0, 2)
    $pct = [math]::Round(($bytesNow / 100MB) * 100, 1)

    $samples.Add([pscustomobject]@{
        Second = $tick
        KBps   = $kbps
        MBTotal = $mbTotal
        Bytes  = $bytesNow
    })

    Write-Host ("{0,3} | {1,10} | {2,10} | {3,10}" -f $tick, $kbps, $mbTotal, $pct)
}

# Drain stderr in case curl reported a network error.
$stderrPath = "$($tmp.FullName).stderr"
$curlErr = ""
if (Test-Path $stderrPath) {
    $curlErr = (Get-Content $stderrPath -Raw).Trim()
    Remove-Item $stderrPath -Force -ErrorAction SilentlyContinue
}

$endedAt = Get-Date
$duration = ($endedAt - $startedAt).TotalSeconds

# Final byte count (curl may have flushed bytes between last tick and exit).
$finalBytes = 0L
if (Test-Path $tmp.FullName) {
    $finalBytes = (Get-Item $tmp.FullName).Length
}
Remove-Item $tmp.FullName -Force -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "=== Summary ===" -ForegroundColor Cyan
Write-Host ("Duration:    {0,8:N1} s" -f $duration)
Write-Host ("Total:       {0,8:N2} MB ({1} bytes)" -f ($finalBytes / 1048576.0), $finalBytes)
if ($duration -gt 0) {
    $avgKBps = ($finalBytes / 1024.0) / $duration
    $avgMbps = $avgKBps * 8 / 1000.0
    Write-Host ("Avg overall: {0,8:N1} KB/s ({1:N2} Mbps)" -f $avgKBps, $avgMbps)
}

if ($samples.Count -gt 0) {
    $rates = $samples | Where-Object { $_.KBps -gt 0 } | Select-Object -ExpandProperty KBps
    if ($rates.Count -gt 0) {
        $sorted = $rates | Sort-Object
        $median = $sorted[[int]($sorted.Count / 2)]
        $p95idx = [int][math]::Floor($sorted.Count * 0.95)
        if ($p95idx -ge $sorted.Count) { $p95idx = $sorted.Count - 1 }
        $p95 = $sorted[$p95idx]
        $max = ($rates | Measure-Object -Maximum).Maximum
        $mean = [math]::Round(($rates | Measure-Object -Average).Average, 1)
        Write-Host ("Per-second (excl. zero ticks):")
        Write-Host ("  mean:      {0,8:N1} KB/s" -f $mean)
        Write-Host ("  median:    {0,8:N1} KB/s" -f $median)
        Write-Host ("  p95:       {0,8:N1} KB/s" -f $p95)
        Write-Host ("  max burst: {0,8:N1} KB/s" -f $max)
    }
}

if ($curlErr) {
    Write-Host ""
    Write-Host "curl reported:" -ForegroundColor Yellow
    Write-Host $curlErr -ForegroundColor Yellow
}

if ($OutFile) {
    $samples | Export-Csv -NoTypeInformation -Path $OutFile -Encoding utf8
    Write-Host ""
    Write-Host "Samples written to $OutFile" -ForegroundColor Green
}
