[CmdletBinding()]
param(
    [string]$Output = "bin\monitor.exe"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
$cachePath = Join-Path $projectRoot ".gocache"
$outputPath = Join-Path $projectRoot $Output

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go was not found in PATH. Install Go or add it to PATH before building."
}

New-Item -ItemType Directory -Path $cachePath -Force | Out-Null
New-Item -ItemType Directory -Path (Split-Path -Parent $outputPath) -Force | Out-Null
$env:GOCACHE = $cachePath

Push-Location $projectRoot
try {
    Write-Host "Building Windows GUI executable..." -ForegroundColor Cyan
    & go build -trimpath '-ldflags=-H=windowsgui -s -w' -o $outputPath .\cmd\monitor
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
}

# Verify the generated PE file uses IMAGE_SUBSYSTEM_WINDOWS_GUI (2).
$bytes = [System.IO.File]::ReadAllBytes($outputPath)
$peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
$subsystemOffset = $peOffset + 4 + 20 + 68
$subsystem = [BitConverter]::ToUInt16($bytes, $subsystemOffset)
if ($subsystem -ne 2) {
    throw "Build completed, but the executable subsystem is $subsystem instead of Windows GUI (2)."
}

$file = Get-Item $outputPath
Write-Host "Build succeeded: $($file.FullName)" -ForegroundColor Green
Write-Host "Size: $([Math]::Round($file.Length / 1MB, 2)) MB; PE subsystem: Windows GUI (2)"
