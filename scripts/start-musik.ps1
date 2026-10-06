#Requires -Version 5.1
<#
.SYNOPSIS
  Start musik on Windows: Python worker (8790) + Go player (8787).

.DESCRIPTION
  The Go player does not read .env itself — this launcher loads .env into its
  own process (without printing values) and hands the environment to both
  programs. A repeated run is idempotent: services that are already healthy
  are left alone.

  worker autostart is disabled in .env (MUSIK_WORKER_AUTOSTART=0), so this
  script is the one that owns the worker process.

.EXAMPLE
  .\scripts\start-musik.ps1               # start worker + player
  .\scripts\start-musik.ps1 -Stop         # stop both
  .\scripts\start-musik.ps1 -InstallStartup   # register logon autostart
#>
[CmdletBinding()]
param(
    [switch]$Stop,
    [switch]$InstallStartup,
    [switch]$UninstallStartup,
    [switch]$WorkerOnly,
    [switch]$PlayerOnly,
    [int]$TimeoutSec = 45
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\musik-env.ps1"

$root = Split-Path -Parent $PSScriptRoot
$runDir = Join-Path $root 'data\run'
$dataDir = Join-Path $root 'data'
$workerPidFile = Join-Path $runDir 'worker.pid'
$playerPidFile = Join-Path $runDir 'player.pid'
$workerLog = Join-Path $dataDir 'worker.log'
$workerErr = Join-Path $dataDir 'worker.err.log'
$playerLog = Join-Path $dataDir 'player.log'
$playerErr = Join-Path $dataDir 'player.err.log'
$taskName = 'Musik'

function Get-TrackedProcess {
    param([string]$PidFile, [string]$MustContain)
    if (-not (Test-Path -LiteralPath $PidFile)) { return $null }
    $raw = (Get-Content -LiteralPath $PidFile -Raw -ErrorAction SilentlyContinue)
    $id = 0
    if (-not [int]::TryParse($raw, [ref]$id)) { return $null }
    $proc = Get-Process -Id $id -ErrorAction SilentlyContinue
    if (-not $proc) { return $null }
    if ($MustContain) {
        try {
            $cmdline = (Get-CimInstance Win32_Process -Filter "ProcessId = $id").CommandLine
        } catch { $cmdline = '' }
        if ($cmdline -and ($cmdline -notlike "*$MustContain*")) { return $null }
    }
    return $proc
}

function Stop-TrackedProcess {
    param([string]$PidFile, [string]$MustContain, [string]$Label)
    $proc = Get-TrackedProcess -PidFile $PidFile -MustContain $MustContain
    if ($proc) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Write-Host "stopped $Label (pid $($proc.Id))"
    } else {
        Write-Host "$Label is not running"
    }
    Remove-Item -LiteralPath $PidFile -ErrorAction SilentlyContinue
}

function Wait-Healthy {
    param([string]$Url, [int]$Seconds)
    for ($i = 0; $i -lt $Seconds; $i++) {
        if (Test-MusikPort -Url $Url) { return $true }
        Start-Sleep -Seconds 1
    }
    return $false
}

if ($InstallStartup) {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' `
        -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" `
        -WorkingDirectory $root
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
        -Settings $settings -Force -Description 'musik: worker + player at logon' | Out-Null
    Write-Host "scheduled task '$taskName' registered (start at logon)"
    return
}

if ($UninstallStartup) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "scheduled task '$taskName' removed"
    return
}

Import-MusikDotEnv -Root $root
if (-not $env:MUSIK_ROOT) { $env:MUSIK_ROOT = $root }
# python child processes must not encode console output as cp1251
# (rich prints spinner chars like ⠋ that cp1251 cannot encode)
$env:PYTHONIOENCODING = 'utf-8'
$env:PYTHONUTF8 = '1'
# pick up system PATH (WinGet ffmpeg lives in the user PATH)
$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($machinePath -and $userPath) { $env:Path = "$machinePath;$userPath" }

$workerUrl = Get-MusikWorkerUrl
$healthUrl = Get-MusikHealthUrl

if ($Stop) {
    Stop-TrackedProcess -PidFile $playerPidFile -MustContain 'musik-player' -Label 'player'
    Stop-TrackedProcess -PidFile $workerPidFile -MustContain 'worker' -Label 'worker'
    return
}

New-Item -ItemType Directory -Force -Path $runDir | Out-Null

if (-not $PlayerOnly) {
    if (Test-MusikPort -Url $workerUrl) {
        Write-Host "worker already healthy at $workerUrl"
    } else {
        $musikExe = Join-Path $root '.venv\Scripts\musik.exe'
        $python = Join-Path $root '.venv\Scripts\python.exe'
        if (Test-Path -LiteralPath $musikExe) {
            $exe = $musikExe; $exeArgs = @('worker')
        } elseif (Test-Path -LiteralPath $python) {
            $exe = $python; $exeArgs = @('-m', 'musik', 'worker')
        } else {
            Write-Error "python not found (expected $musikExe); install the venv first"
            exit 1
        }
        $existing = Get-TrackedProcess -PidFile $workerPidFile -MustContain 'worker'
        if ($existing) {
            Write-Host "worker process already running (pid $($existing.Id)); waiting for health"
        } else {
            $p = Start-Process -FilePath $exe -ArgumentList $exeArgs -WorkingDirectory $root `
                -RedirectStandardOutput $workerLog -RedirectStandardError $workerErr `
                -WindowStyle Hidden -PassThru
            Set-Content -LiteralPath $workerPidFile -Value $p.Id
            Write-Host "worker started pid=$($p.Id) (log: $workerLog)"
        }
        if (-not (Wait-Healthy -Url $workerUrl -Seconds $TimeoutSec)) {
            Write-Error "worker did not become healthy in ${TimeoutSec}s — check $workerErr"
            exit 1
        }
        Write-Host "worker healthy at $workerUrl"
    }
}

if (-not $WorkerOnly) {
    if (Test-MusikPort -Url $healthUrl) {
        Write-Host "player already healthy at $healthUrl"
    } else {
        $playerExe = Join-Path $root 'player\bin\musik-player.exe'
        if (-not (Test-Path -LiteralPath $playerExe)) {
            Write-Error "player binary missing: $playerExe (build it with: cd player && go build -o bin\musik-player.exe ./cmd/musik-player)"
            exit 1
        }
        $existing = Get-TrackedProcess -PidFile $playerPidFile -MustContain 'musik-player'
        if ($existing) {
            Write-Host "player process already running (pid $($existing.Id)); waiting for health"
        } else {
            $p = Start-Process -FilePath $playerExe -WorkingDirectory $root `
                -RedirectStandardOutput $playerLog -RedirectStandardError $playerErr `
                -WindowStyle Hidden -PassThru
            Set-Content -LiteralPath $playerPidFile -Value $p.Id
            Write-Host "player started pid=$($p.Id) (log: $playerLog)"
        }
        if (-not (Wait-Healthy -Url $healthUrl -Seconds $TimeoutSec)) {
            Write-Error "player did not become healthy in ${TimeoutSec}s — check $playerErr"
            exit 1
        }
        Write-Host "player healthy at $healthUrl"
    }
}

Write-Host ''
Write-Host "musik is up:"
Write-Host "  player  $healthUrl"
Write-Host "  worker  $workerUrl"
if ($env:MUSIK_PUBLIC_BASE_URL) { Write-Host "  public  $($env:MUSIK_PUBLIC_BASE_URL)" }
