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

if ($Once) {
    if (Invoke-Rescan) { exit 0 }
    exit 1
}

Write-Host "auto-scan every $IntervalMin min via $scanUrl (Ctrl+C to stop)"
while ($true) {
    $null = Invoke-Rescan
    Start-Sleep -Seconds ($IntervalMin * 60)
}
