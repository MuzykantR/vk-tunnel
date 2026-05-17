# Downloads wintun.dll (amd64) for embedding into vk-client.
$ErrorActionPreference = "Stop"
$version = "0.14.1"
$outDir = Join-Path $PSScriptRoot "..\wintun\dll"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$zip = Join-Path $env:TEMP "wintun-$version.zip"
$extract = Join-Path $env:TEMP "wintun-extract"
Invoke-WebRequest -Uri "https://www.wintun.net/builds/wintun-$version.zip" -OutFile $zip -UseBasicParsing
if (Test-Path $extract) { Remove-Item -Recurse -Force $extract }
Expand-Archive -Path $zip -DestinationPath $extract -Force
Copy-Item (Join-Path $extract "wintun\bin\amd64\wintun.dll") (Join-Path $outDir "wintun-amd64.dll") -Force
Write-Host "OK: $(Join-Path $outDir 'wintun-amd64.dll')"
