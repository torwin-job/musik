# Shared helper: load .env into the current process without printing values.
# Dot-source from other scripts:  . "$PSScriptRoot\musik-env.ps1"

function Import-MusikDotEnv {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Root
    )
    $path = Join-Path $Root '.env'
    if (-not (Test-Path -LiteralPath $path)) {
        Write-Warning ".env not found at $path"
        return
    }
    foreach ($line in Get-Content -LiteralPath $path) {
        $t = $line.Trim()
        if (-not $t -or $t.StartsWith('#')) { continue }
        $i = $t.IndexOf('=')
        if ($i -lt 1) { continue }
        $key = $t.Substring(0, $i).Trim()
        if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { continue }
        $val = $t.Substring($i + 1).Trim()
        if ($val.Length -ge 2) {
            $q = $val[0]
            if (($q -eq '"' -or $q -eq "'") -and $val[$val.Length - 1] -eq $q) {
                $val = $val.Substring(1, $val.Length - 2)
            }
        }
        # real environment wins over .env (same as pydantic-settings)
        if (-not (Test-Path -LiteralPath "Env:$key")) {
            Set-Item -LiteralPath "Env:$key" -Value $val
        }
    }
}

function Get-MusikHealthUrl {
    # MUSIK_PLAYER_ADDR is usually 0.0.0.0:8787; probe it on loopback.
    $addr = $env:MUSIK_PLAYER_ADDR
    if (-not $addr) { $addr = ':8787' }
    $addr = $addr -replace '^0\.0\.0\.0', '127.0.0.1' -replace '^:', '127.0.0.1:'
    if ($addr -notmatch '^https?://') { $addr = "http://$addr" }
    return "$addr/api/health"
}

function Get-MusikWorkerUrl {
    $url = $env:MUSIK_WORKER_URL
    if (-not $url) { $url = 'http://127.0.0.1:8790' }
    return ($url.TrimEnd('/') + '/jobs')
}

function Test-MusikPort {
    param([Parameter(Mandatory = $true)][string]$Url)
    try {
        $null = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3
        return $true
    } catch {
        return $false
    }
}
