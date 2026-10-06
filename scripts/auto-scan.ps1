#Requires -Version 5.1
<#
.SYNOPSIS
  Auto-rescan of the music library through the player API.

.DESCRIPTION
  Calls POST /api/library/rescan (with Bearer MUSIK_API_TOKEN when set) either
  once or on an interval. -InstallTask registers a scheduled task so the scan
  happens without anyone watching.

.EXAMPLE
  .\scripts\auto-scan.ps1 -Once            # single rescan now
  .\scripts\auto-scan.ps1                  # loop every 60 min
  .\scripts\auto-scan.ps1 -InstallTask -IntervalMin 30
#>
[CmdletBinding()]
param(
    [switch]$Once,
    [int]$IntervalMin = 60,
    [switch]$InstallTask,
    [switch]$UninstallTask,
    [int]$TimeoutSec = 20
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\musik-env.ps1"

$root = Split-Path -Parent $PSScriptRoot
$taskName = 'MusikAutoScan'

if ($InstallTask) {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' `
        -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`" -Once" `
        -WorkingDirectory $root
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(5) `
        -RepetitionInterval (New-TimeSpan -Minutes $IntervalMin)
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
        -Settings $settings -Force -Description "musik: library rescan every $IntervalMin min" | Out-Null
    Write-Host "scheduled task '$taskName' registered (every $IntervalMin min)"
    return
}

if ($UninstallTask) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "scheduled task '$taskName' removed"
    return
}

Import-MusikDotEnv -Root $root

$health = Get-MusikHealthUrl
$baseUrl = $health -replace '/api/health$', ''
$scanUrl = "$baseUrl/api/library/rescan"

$headers = @{}
if ($env:MUSIK_API_TOKEN) { $headers['Authorization'] = "Bearer $($env:MUSIK_API_TOKEN)" }

function Write-ScanLog {
    param([string]$Message)
    Write-Host $Message
    $logFile = Join-Path $root 'data\auto-scan.log'
    try { Add-Content -LiteralPath $logFile -Value $Message -Encoding UTF8 } catch { }
}

function Invoke-Rescan {
    $stamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
    try {
        $resp = Invoke-WebRequest -Uri $scanUrl -Method POST -Headers $headers `
            -UseBasicParsing -TimeoutSec $TimeoutSec
        Write-ScanLog "[$stamp] rescan OK $($resp.StatusCode): $($resp.Content)"
        return $true
    } catch {
        if ($_.Exception.Response) {
            $code = [int]$_.Exception.Response.StatusCode
            Write-ScanLog "[$stamp] rescan failed HTTP $code — is the player running? ($scanUrl)"
        } else {
            Write-ScanLog "[$stamp] rescan failed: $($_.Exception.Message)"
        }
        return $false
    }
}

function Invoke-LibraryArt {
    # Online album covers (iTunes) and artist photos (Deezer). Both commands
    # skip what is already there and remember misses for 30 days, so an
    # hourly run only asks about new albums and artists. Albums the running
    # scan is still adding are picked up on the next run.
    $musik = Join-Path $root '.venv\Scripts\musik.exe'
    if (-not (Test-Path -LiteralPath $musik)) {
        Write-ScanLog "art skipped: $musik not found"
        return
    }
    $env:PYTHONIOENCODING = 'utf-8'
    $env:PYTHONUTF8 = '1'
    $ErrorActionPreference = 'Continue'
    foreach ($cmd in @('artwork', 'artist-photos')) {
        $stamp = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
        try {
            $out = & $musik $cmd 2>&1 | Out-String
            $summary = ($out -split "`r?`n" | Where-Object { $_ -match '(folders|queued|found|missing|failed)\s' } |
                ForEach-Object { ($_ -replace '[^\w\s]', ' ').Trim() -replace '\s+', '=' }) -join ' '
            Write-ScanLog "[$stamp] $cmd exit $LASTEXITCODE $summary"
        } catch {
            Write-ScanLog "[$stamp] $cmd failed: $($_.Exception.Message)"
        }
    }
    try {
        $null = Invoke-WebRequest -Uri "$baseUrl/api/reload" -Method POST -Headers $headers `
            -UseBasicParsing -TimeoutSec $TimeoutSec
    } catch {
        Write-ScanLog "reload after art failed: $($_.Exception.Message)"
    }
}

if ($Once) {
    $ok = Invoke-Rescan
    Invoke-LibraryArt
    if ($ok) { exit 0 }
    exit 1
}

Write-Host "auto-scan every $IntervalMin min via $scanUrl (Ctrl+C to stop)"
while ($true) {
    $null = Invoke-Rescan
    Invoke-LibraryArt
    Start-Sleep -Seconds ($IntervalMin * 60)
}
