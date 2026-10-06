#Requires -Version 5.1
<#
.SYNOPSIS
  Set up musik on this Windows PC with one command: clone the repository,
  install what is missing, configure .env, build, start, optional autostart.

.DESCRIPTION
  Run it from anywhere (it downloads the code) or from an existing clone
  (it uses that clone). Asks only what it cannot decide itself: where to put
  musik, where the music is, whether the phone should reach the server over
  Wi-Fi, a password, and autostart. -Yes accepts every default.

  Repeated runs are safe: the code is updated with git pull, an existing
  .env is kept, the server is restarted with the new build.

.EXAMPLE
  # one command in PowerShell (downloads this script and runs it):
  irm https://raw.githubusercontent.com/torwin-job/musik/main/scripts/setup-windows.ps1 -OutFile $env:TEMP\musik-setup.ps1; powershell -ExecutionPolicy Bypass -File $env:TEMP\musik-setup.ps1

.EXAMPLE
  .\scripts\setup-windows.ps1 -Dir D:\musik -Music D:\Music -Yes
#>
[CmdletBinding()]
param(
    [string]$Dir,
    [string]$Repo = 'https://github.com/torwin-job/musik.git',
    [string]$Branch = 'main',
    [string]$Music,
    [int]$Port = 8787,
    [switch]$Yes
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Console]::OutputEncoding = [Text.Encoding]::UTF8 } catch { }
$env:PYTHONIOENCODING = 'utf-8'
$env:PYTHONUTF8 = '1'

function Say([string]$m) { Write-Host ''; Write-Host "==> $m" -ForegroundColor Cyan }
function Info([string]$m) { Write-Host "    $m" }
function Warn([string]$m) { Write-Host "    ! $m" -ForegroundColor Yellow }
function Fail([string]$m) { Write-Host "    x $m" -ForegroundColor Red; exit 1 }

function Ask([string]$Question, [string]$Default) {
    if ($Yes) { return $Default }
    $a = Read-Host "    $Question [$Default]"
    if ([string]::IsNullOrWhiteSpace($a)) { return $Default }
    return $a.Trim().Trim('"')
}

function Confirm-Step([string]$Question, [bool]$Default = $true) {
    if ($Yes) { return $Default }
    $hint = if ($Default) { 'Y/n' } else { 'y/N' }
    while ($true) {
        $a = Read-Host "    $Question [$hint]"
        if ([string]::IsNullOrWhiteSpace($a)) { return $Default }
        switch -Regex ($a.Trim()) {
            '^(y|yes|д|да)$' { return $true }
            '^(n|no|н|нет)$' { return $false }
        }
    }
}

function Update-SessionPath {
    $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $user = [Environment]::GetEnvironmentVariable('Path', 'User')
    $env:Path = "$machine;$user"
}

function Install-WithWinget([string]$Id, [string]$Name, [string]$Url) {
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        Fail "$Name не найден, а winget недоступен. Установи $Name вручную: $Url — и запусти скрипт снова."
    }
    if (-not (Confirm-Step "$Name не найден. Установить через winget?")) {
        Fail "$Name нужен для работы. Установи его ($Url) и запусти скрипт снова."
    }
    & winget install --id $Id -e --source winget --accept-package-agreements --accept-source-agreements
    # -1978335189: already installed / no applicable upgrade
    if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne -1978335189) {
        Fail "winget не смог установить $Name (код $LASTEXITCODE). Установи вручную: $Url"
    }
    Update-SessionPath
}

function Get-PythonCommand {
    $candidates = @(@('py', '-3.13'), @('py', '-3.12'), @('py', '-3.11'), @('python'), @('python3'))
    foreach ($c in $candidates) {
        if (-not (Get-Command $c[0] -ErrorAction SilentlyContinue)) { continue }
        $rest = @($c | Select-Object -Skip 1)
        try {
            $v = & $c[0] @rest -c "import sys; print('%d.%d' % sys.version_info[:2])" 2>$null
        } catch { continue }
        if ($LASTEXITCODE -ne 0 -or -not $v) { continue }
        $parts = "$v".Trim().Split('.')
        if ([int]$parts[0] -eq 3 -and [int]$parts[1] -ge 11) { return , $c }
    }
    return $null
}

function Test-GoVersion {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { return $false }
    $v = (& go version) -replace '^go version go(\d+)\.(\d+).*$', '$1.$2'
    $parts = $v.Split('.')
    # Go 1.21+ downloads the exact toolchain go.mod asks for by itself.
    return ([int]$parts[0] -gt 1) -or ([int]$parts[1] -ge 21)
}

function New-Secret([int]$Bytes = 32) {
    $b = New-Object byte[] $Bytes
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
    return -join ($b | ForEach-Object { $_.ToString('x2') })
}

function New-Password {
    $alphabet = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'
    $b = New-Object byte[] 14
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
    return -join ($b | ForEach-Object { $alphabet[$_ % $alphabet.Length] })
}

function Get-LanIp {
    try {
        $cfg = Get-NetIPConfiguration -ErrorAction Stop |
            Where-Object { $_.IPv4DefaultGateway -and $_.NetAdapter.Status -eq 'Up' } |
            Select-Object -First 1
        if ($cfg) { return @($cfg.IPv4Address)[0].IPAddress }
    } catch { }
    return $null
}

function Test-PortFree([int]$P) {
    try {
        $l = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Any, $P)
        $l.Start(); $l.Stop()
        return $true
    } catch { return $false }
}

# Same rules as the player's settings page: rewrite KEY= lines in place,
# keep comments, append missing keys; single quotes keep Windows paths intact.
function Set-EnvValues([string]$Path, [hashtable]$Values) {
    $lines = New-Object System.Collections.Generic.List[string]
    if (Test-Path -LiteralPath $Path) {
        foreach ($l in [IO.File]::ReadAllLines($Path)) { $lines.Add($l) }
    }
    $done = @{}
    $render = {
        param($k, $v)
        if ($v -match "[\s#'""]") { return "$k='" + ($v -replace "'", '') + "'" }
        return "$k=$v"
    }
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $t = $lines[$i].Trim()
        $commented = $t.StartsWith('#')
        $body = $t.TrimStart('#').Trim()
        $eq = $body.IndexOf('=')
        if ($eq -lt 1) { continue }
        $key = $body.Substring(0, $eq).Trim()
        if (-not $Values.ContainsKey($key) -or $done[$key]) { continue }
        if ($commented -and ($lines | Where-Object { $_.Trim() -match "^$([regex]::Escape($key))=" })) { continue }
        $lines[$i] = & $render $key $Values[$key]
        $done[$key] = $true
    }
    foreach ($k in ($Values.Keys | Sort-Object)) {
        if (-not $done[$k]) { $lines.Add((& $render $k $Values[$k])) }
    }
    [IO.File]::WriteAllText($Path, (($lines -join "`r`n") + "`r`n"), (New-Object Text.UTF8Encoding $false))
}

function Read-EnvValue([string]$Path, [string]$Key) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    foreach ($l in [IO.File]::ReadAllLines($Path)) {
        $t = $l.Trim()
        if ($t.StartsWith('#') -or $t.IndexOf('=') -lt 1) { continue }
        $k, $v = $t.Split('=', 2)
        if ($k.Trim() -eq $Key) { return $v.Trim().Trim("'").Trim('"') }
    }
    return $null
}

Write-Host ''
Write-Host 'musik — установка на Windows' -ForegroundColor Green
Write-Host '    Ответы в [скобках] — значения по умолчанию, Enter их принимает.'

# 1. Tools ---------------------------------------------------------------
Say 'Проверяю Git, Python, Go и ffmpeg'
Update-SessionPath
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    Install-WithWinget 'Git.Git' 'Git' 'https://git-scm.com/download/win'
}
Info ("git     " + ((& git --version) -replace '^git version ', ''))
$py = Get-PythonCommand
if (-not $py) {
    Install-WithWinget 'Python.Python.3.12' 'Python 3.12' 'https://www.python.org/downloads/windows/'
    $py = Get-PythonCommand
    if (-not $py) { Fail 'Python 3.11+ так и не нашёлся. Открой новое окно PowerShell и запусти скрипт ещё раз.' }
}
$pyExe = $py[0]; $pyArgs = @($py | Select-Object -Skip 1)
Info ("python  " + (& $pyExe @pyArgs --version))
if (-not (Test-GoVersion)) {
    Install-WithWinget 'GoLang.Go' 'Go' 'https://go.dev/dl/'
    if (-not (Test-GoVersion)) { Fail 'Go 1.21+ так и не нашёлся. Открой новое окно PowerShell и запусти скрипт ещё раз.' }
}
Info ("go      " + ((& go version) -replace '^go version ', ''))
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
    Install-WithWinget 'Gyan.FFmpeg' 'ffmpeg' 'https://www.gyan.dev/ffmpeg/builds/'
}
if (Get-Command ffmpeg -ErrorAction SilentlyContinue) { Info 'ffmpeg  есть' } else { Warn 'ffmpeg не найден в PATH — поделиться радио и мобильный поток работать не будут.' }

# 2. Code ----------------------------------------------------------------
Say 'Код musik'
$here = $null
if ($PSScriptRoot -and (Test-Path -LiteralPath (Join-Path $PSScriptRoot '..\player\go.mod'))) {
    $here = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
}
if (-not $Dir) {
    $default = if ($here) { $here } else { Join-Path $env:USERPROFILE 'musik' }
    $Dir = Ask 'Куда установить musik' $default
}
$Dir = [IO.Path]::GetFullPath($Dir)
if (Test-Path -LiteralPath (Join-Path $Dir '.git')) {
    Info "уже установлен в $Dir"
    if (Confirm-Step 'Обновить код до последней версии (git pull)?') {
        & git -C $Dir pull --ff-only
        if ($LASTEXITCODE -ne 0) { Warn 'git pull не удался (есть локальные изменения?) — продолжаю с текущим кодом.' }
    }
} elseif ((Test-Path -LiteralPath $Dir) -and (Get-ChildItem -LiteralPath $Dir -Force | Select-Object -First 1)) {
    Fail "Папка $Dir не пустая и это не musik. Укажи другую: -Dir <папка>"
} else {
    & git clone --branch $Branch $Repo $Dir
    if ($LASTEXITCODE -ne 0) { Fail "Не удалось скачать $Repo" }
}
Set-Location -LiteralPath $Dir

# 3. Python environment ---------------------------------------------------
Say 'Python-окружение (первый раз качает ~1 ГБ: torch для анализа музыки)'
$venvPy = Join-Path $Dir '.venv\Scripts\python.exe'
if (-not (Test-Path -LiteralPath $venvPy)) {
    & $pyExe @pyArgs -m venv (Join-Path $Dir '.venv')
    if ($LASTEXITCODE -ne 0) { Fail 'Не удалось создать .venv' }
}
& $venvPy -m pip install --upgrade pip --quiet
& $venvPy -m pip install -e $Dir
if ($LASTEXITCODE -ne 0) { Fail 'pip install не удался — смотри ошибку выше.' }
$musikExe = Join-Path $Dir '.venv\Scripts\musik.exe'

# 4. Settings (.env) ------------------------------------------------------
Say 'Настройки'
$envPath = Join-Path $Dir '.env'
$generatedPassword = $null
if (Test-Path -LiteralPath $envPath) {
    Info '.env уже есть — оставляю его. Адрес и папку с музыкой можно поменять в веб-интерфейсе: Профиль → Настройки.'
    $Port = 8787
    $addr = Read-EnvValue $envPath 'MUSIK_PLAYER_ADDR'
    if ($addr -and $addr -match ':(\d+)$') { $Port = [int]$Matches[1] }
} else {
    if (-not $Music) { $Music = Ask 'Папка с музыкой' (Join-Path $env:USERPROFILE 'Music') }
    $Music = [IO.Path]::GetFullPath($Music)
    if (-not (Test-Path -LiteralPath $Music)) {
        if (Confirm-Step "Папки $Music нет. Создать?") { New-Item -ItemType Directory -Force -Path $Music | Out-Null }
        else { Fail 'Нужна папка с музыкой.' }
    }
    while (-not (Test-PortFree $Port)) {
        Warn "порт $Port занят"
        $Port = [int](Ask 'Другой порт' ([string]($Port + 1)))
    }
    $lan = Confirm-Step 'Открыть доступ с телефона и других устройств в домашней сети (Wi-Fi)?'
    $ip = Get-LanIp
    if (-not $Yes) {
        $pw = Read-Host '    Пароль для входа (Enter — придумать автоматически)' -AsSecureString
        $plain = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($pw))
    } else { $plain = '' }
    if ([string]::IsNullOrWhiteSpace($plain)) { $plain = New-Password; $generatedPassword = $plain }
    $values = @{
        MUSIK_PASSWORD         = $plain
        MUSIK_API_TOKEN        = New-Secret
        MUSIK_SESSION_SECRET   = New-Secret
        MUSIK_LIBRARY          = $Music
        MUSIK_PLAYER_ADDR      = $(if ($lan) { "0.0.0.0:$Port" } else { "127.0.0.1:$Port" })
        # start-musik.ps1 starts the worker; the player must not start a second one.
        MUSIK_WORKER_AUTOSTART = '0'
    }
    if ($lan -and $ip) { $values['MUSIK_PUBLIC_BASE_URL'] = "http://${ip}:$Port" }
    Copy-Item -LiteralPath (Join-Path $Dir '.env.example') -Destination $envPath
    Set-EnvValues $envPath $values
    Info ".env создан: $envPath"
}

# 5. Database and build ----------------------------------------------------
Say 'База данных'
& $musikExe db migrate
if ($LASTEXITCODE -ne 0) { Fail 'musik db migrate не удался.' }

Say 'Сборка плеера (Go)'
$wasRunning = Get-Process musik-player -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq (Join-Path $Dir 'player\bin\musik-player.exe') }
if ($wasRunning) {
    # The exe is locked while it runs.
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Dir 'scripts\start-musik.ps1') -Stop | Out-Null
}
Push-Location -LiteralPath (Join-Path $Dir 'player')
try {
    & go build -o bin\musik-player.exe ./cmd/musik-player
    if ($LASTEXITCODE -ne 0) { Fail 'go build не удался — смотри ошибку выше.' }
} finally { Pop-Location }

# 6. Firewall --------------------------------------------------------------
$listenAddr = Read-EnvValue $envPath 'MUSIK_PLAYER_ADDR'
$lanEnabled = $listenAddr -and -not ($listenAddr -match '^(127\.0\.0\.1|localhost):')
if ($lanEnabled) {
    Say 'Брандмауэр'
    $ruleName = "musik $Port"
    if (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue) {
        Info "правило «$ruleName» уже есть"
    } elseif (Confirm-Step "Разрешить входящие подключения на порт $Port, чтобы открывался телефон? (попросит права администратора)") {
        $cmd = "New-NetFirewallRule -DisplayName '$ruleName' -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow -Profile Private,Domain | Out-Null"
        $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
        try {
            if ($admin) { Invoke-Expression $cmd }
            else { Start-Process powershell -Verb RunAs -Wait -ArgumentList '-NoProfile', '-Command', $cmd }
            Info 'правило добавлено'
        } catch { Warn "не получилось: $($_.Exception.Message)" }
    }
    try {
        $public = Get-NetConnectionProfile -ErrorAction Stop | Where-Object { $_.NetworkCategory -eq 'Public' }
        if ($public) { Warn "Сеть «$($public[0].Name)» помечена как общественная — Windows не пустит к серверу телефон. Сделай её частной: Параметры → Сеть и Интернет → свойства сети → Частная." }
    } catch { }
}

# 7. Start ----------------------------------------------------------------------
Say 'Запуск'
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Dir 'scripts\start-musik.ps1') -Restart
if ($LASTEXITCODE -ne 0) { Fail 'Сервер не запустился — логи: data\worker.err.log и data\player.err.log' }

# 8. Autostart ------------------------------------------------------------------
Say 'Автозапуск'
if (Confirm-Step 'Запускать musik автоматически при входе в Windows?') {
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Dir 'scripts\start-musik.ps1') -InstallStartup
}
if (Confirm-Step 'Каждый час проверять папку с музыкой на новые файлы и подтягивать обложки?') {
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Dir 'scripts\auto-scan.ps1') -InstallTask
}

# 9. First scan -------------------------------------------------------------------
$localUrl = "http://127.0.0.1:$Port"
if (Confirm-Step 'Просканировать музыку сейчас? (первый раз долго: каждый трек анализируется)') {
    $token = Read-EnvValue $envPath 'MUSIK_API_TOKEN'
    try {
        $null = Invoke-WebRequest -Uri "$localUrl/api/library/rescan" -Method POST -UseBasicParsing `
            -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 20
        Info 'сканирование запущено, прогресс — в веб-интерфейсе'
    } catch { Warn "не удалось запустить: $($_.Exception.Message)" }
}

# Summary ----------------------------------------------------------------------------
$public = Read-EnvValue $envPath 'MUSIK_PUBLIC_BASE_URL'
Write-Host ''
Write-Host 'Готово!' -ForegroundColor Green
Info "На этом компьютере:  $localUrl"
if ($public) { Info "С телефона (Wi-Fi):  $public" }
if ($generatedPassword) { Info "Пароль для входа:    $generatedPassword   (запиши его; он же в $envPath)" }
Info 'Токен для приложения на телефоне и адреса — в веб-интерфейсе: Профиль → Настройки.'
Info "Управление: $Dir\scripts\start-musik.ps1 [-Stop | -Restart]"
