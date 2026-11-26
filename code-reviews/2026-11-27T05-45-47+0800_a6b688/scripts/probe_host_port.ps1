# probe_host_port: HOST is only used when PORT is also set. With HOST=127.0.0.1 and
# no PORT, main.go falls back to the constant ":8088" and listens on every
# interface, so the operator's HOST is silently ignored.
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
$stamp = [guid]::NewGuid().ToString('N')
$exe = Join-Path $env:TEMP ('mesh-server-probe-' + $stamp + '.exe')
$dir = Join-Path $env:TEMP ('mesh-server-probe-' + $stamp)
$out = Join-Path $env:TEMP ('mesh-server-probe-' + $stamp + '.out.log')
$err = Join-Path $env:TEMP ('mesh-server-probe-' + $stamp + '.err.log')

Push-Location $repo
try {
    & go build -o $exe .
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally {
    Pop-Location
}
New-Item -ItemType Directory -Force $dir | Out-Null

$env:HOST = '127.0.0.1'
Remove-Item Env:PORT -ErrorAction SilentlyContinue
$env:DATA_DIR = $dir
$env:AUTH_TOKEN = 'probe_token_0123456789'
$env:SESSION_SIGNING_KEY = 'probe_session_key_0123456789abcdef'
Remove-Item Env:READYZ_TOKEN -ErrorAction SilentlyContinue

$proc = Start-Process -FilePath $exe -RedirectStandardOutput $out -RedirectStandardError $err -PassThru -WindowStyle Hidden
try {
    $line = $null
    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Milliseconds 250
        if (Test-Path $out) {
            $line = Select-String -Path $out -Pattern '"server started"' -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($line) { break }
        }
    }
    if (-not $line) { throw ('server did not start: ' + (Get-Content $err -Raw -ErrorAction SilentlyContinue)) }
    Write-Output ('LOG    ' + $line.Line.Trim())
    Get-NetTCPConnection -LocalPort 8088 -State Listen -ErrorAction SilentlyContinue |
        ForEach-Object { Write-Output ('LISTEN ' + $_.LocalAddress + ':' + $_.LocalPort) }
} finally {
    if (-not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
    Remove-Item Env:HOST, Env:DATA_DIR, Env:AUTH_TOKEN, Env:SESSION_SIGNING_KEY -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $dir, $exe, $out, $err -ErrorAction SilentlyContinue
}
