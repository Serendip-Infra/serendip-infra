<#
.SYNOPSIS
  Start a local dev Serendip: the kind cluster's container, then serendipd
  on the host, serving the console at http://localhost:<port>.

.DESCRIPTION
  serendipd isn't in the cluster's container: it's a host process, so
  `docker start` alone leaves the console down. This script:
    1. starts the kind node container and waits for its API server;
    2. builds the console (if it isn't built yet, or with -BuildUI) and
       serendipd into <StateDir>\bin;
    3. starts serendipd in the background with the state dir's settings,
       logging to <StateDir>\serendipd.log;
    4. waits until the console answers.

  Everything that must survive a session lives in the state dir, never in
  the repo: the kubeconfig, the store, OpenBao's unseal file, API keys.

  State dir layout (defaults; dev-up.env can override any of them):
    kubeconfig        kubeconfig
    store             serendipd.db
    OpenBao unseal    openbao-init.json
    API keys          api-keys.env          (lines: name=token; optional)

  dev-up.env in the state dir holds KEY=VALUE lines. Relative paths are
  resolved against the state dir. Keys: Kubeconfig, Db, OpenBaoCredentials,
  ApiKeysFile, Addr.

  Two dev clusters exist (specs/025-networking T005):
    serendip-t018   kindnet, state ~\.serendip-t018 (the default), :8088;
                    the "external" networking case
    serendip-t025   Cilium, state ~\.serendip-t025-dev, :8089; created with
                    CNI=cilium profiles/edge-1/bench/kind-t470-setup.sh up
                    serendip-t025 <state>\kubeconfig (KIND_CONFIG maps the
                    gateway to host port 30083)

.PARAMETER StateDir
  Folder holding the dev cluster's state. Default: ~\.serendip-t018.

.PARAMETER Cluster
  kind cluster name; its node container is <Cluster>-control-plane.

.PARAMETER Restart
  If a serendipd already listens on the port, stop it and start a fresh
  build. Without it, a busy port is reported and nothing changes.

.PARAMETER BuildUI
  Rebuild the console (pnpm -C ui build) even if a build exists.

.PARAMETER NoBuild
  Don't build: run the existing <StateDir>\bin\serendipd.exe.

.EXAMPLE
  .\scripts\dev-up.ps1
.EXAMPLE
  .\scripts\dev-up.ps1 -Restart -BuildUI
.EXAMPLE
  # The Cilium dev cluster:
  .\scripts\dev-up.ps1 -StateDir ~\.serendip-t025-dev -Cluster serendip-t025
.EXAMPLE
  # Stop it:
  Stop-Process -Id (Get-Content ~\.serendip-t018\serendipd.pid)
#>
[CmdletBinding()]
param(
    [string]$StateDir = (Join-Path $HOME '.serendip-t018'),
    [string]$Cluster = 'serendip-t018',
    [switch]$Restart,
    [switch]$BuildUI,
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Fail($msg) { Write-Host "dev-up: $msg" -ForegroundColor Red; exit 1 }

if (-not (Test-Path $StateDir)) { Fail "state dir $StateDir not found (pass -StateDir)" }
$StateDir = (Resolve-Path $StateDir).Path

# --- Settings: defaults, then dev-up.env -----------------------------------
$cfg = @{
    Kubeconfig         = 'kubeconfig'
    Db                 = 'serendipd.db'
    OpenBaoCredentials = 'openbao-init.json'
    ApiKeysFile        = 'api-keys.env'
    Addr               = '127.0.0.1:8088'
}
$envFile = Join-Path $StateDir 'dev-up.env'
if (Test-Path $envFile) {
    foreach ($line in Get-Content $envFile) {
        $t = $line.Trim()
        if ($t -eq '' -or $t.StartsWith('#')) { continue }
        $k, $v = $t -split '=', 2
        if (-not $cfg.ContainsKey($k.Trim())) { Fail "dev-up.env: unknown key '$($k.Trim())'" }
        $cfg[$k.Trim()] = $v.Trim()
    }
}
function InState($p) { if ([IO.Path]::IsPathRooted($p)) { $p } else { Join-Path $StateDir $p } }
$kubeconfig = InState $cfg.Kubeconfig
$db = InState $cfg.Db
$openbao = InState $cfg.OpenBaoCredentials
$keysFile = InState $cfg.ApiKeysFile
$addr = $cfg.Addr
$port = [int]($addr -split ':')[-1]

if (-not (Test-Path $kubeconfig)) { Fail "kubeconfig $kubeconfig not found (set Kubeconfig= in $envFile)" }
if (-not (Test-Path $openbao)) {
    Write-Warning "OpenBao unseal file $openbao not found: secrets won't work until it exists."
}

# --- Port: don't start a second serendipd ----------------------------------
$listener = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
    $name = if ($owner) { $owner.ProcessName } else { '?' }
    if (-not $Restart) {
        Fail "port $port is already used by $name (PID $($listener.OwningProcess)). Open http://localhost:$port, or re-run with -Restart."
    }
    if ($name -notlike 'serendipd*') {
        Fail "port $port is used by $name (PID $($listener.OwningProcess)), not serendipd; not stopping it."
    }
    Step "Stopping serendipd (PID $($owner.Id))"
    Stop-Process -Id $owner.Id
    $owner.WaitForExit(15000) | Out-Null
}

# --- 1. Cluster container ---------------------------------------------------
$node = "$Cluster-control-plane"
Step "Starting $node"
$state = docker inspect -f '{{.State.Status}}' $node 2>$null
if ($LASTEXITCODE -ne 0) { Fail "container $node not found (is Docker running? is -Cluster right?)" }
if ($state -ne 'running') {
    docker start $node | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail "docker start $node failed" }
}

# kind publishes the API server on a fixed host port; check the kubeconfig
# still points at it.
$published = (docker port $node 6443/tcp 2>$null | Select-Object -First 1)
if ($published) {
    $hostPort = ($published -split ':')[-1]
    if (-not (Select-String -Path $kubeconfig -Pattern ":$hostPort\b" -Quiet)) {
        Write-Warning "kubeconfig $kubeconfig doesn't name the API server's host port $hostPort; serendipd won't reach the cluster."
    }
}

Step 'Waiting for the API server'
$deadline = (Get-Date).AddSeconds(180)
do {
    $ready = docker exec $node kubectl --kubeconfig=/etc/kubernetes/admin.conf get --raw=/readyz 2>$null
    if ($ready -eq 'ok') { break }
    Start-Sleep -Seconds 3
} while ((Get-Date) -lt $deadline)
if ($ready -ne 'ok') { Fail "API server in $node not ready after 180 s" }

# Sign-in needs ThunderID. Don't block on it: serendipd waits for providers
# itself, and the console reports "not ready" meanwhile.
docker exec $node kubectl --kubeconfig=/etc/kubernetes/admin.conf -n serendip-identity `
    wait --for=condition=Available deployment --all --timeout=120s 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Warning 'Identity (ThunderID) not available yet; sign-in will work once it is.' }

# --- 2. Build -----------------------------------------------------------------
$bin = Join-Path $StateDir 'bin\serendipd.exe'
if (-not $NoBuild) {
    $index = Join-Path $repo 'cmd\serendipd\console\dist\app\index.html'
    if ($BuildUI -or -not (Test-Path $index)) {
        Step 'Building the console'
        $ui = Join-Path $repo 'ui'
        if (Get-Command pnpm -ErrorAction SilentlyContinue) {
            pnpm -C $ui build
        } elseif (Get-Command corepack -ErrorAction SilentlyContinue) {
            corepack pnpm -C $ui build
        } else {
            Fail 'neither pnpm nor corepack found (needed to build the console)'
        }
        if ($LASTEXITCODE -ne 0) { Fail 'console build failed' }
    }
    Step 'Building serendipd'
    New-Item -ItemType Directory -Force (Split-Path $bin) | Out-Null
    Push-Location $repo
    try { go build -o $bin ./cmd/serendipd } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { Fail 'go build failed' }
} elseif (-not (Test-Path $bin)) {
    Fail "-NoBuild, but $bin doesn't exist"
}

# --- 3. Start serendipd -------------------------------------------------------
# API keys: name=token lines -> SERENDIP_API_KEYS "token:name,...".
$keys = ''
if (Test-Path $keysFile) {
    $keys = (Get-Content $keysFile | Where-Object { $_ -match '=' -and -not $_.TrimStart().StartsWith('#') } | ForEach-Object {
            $n, $tok = $_ -split '=', 2; "$($tok.Trim()):$($n.Trim())"
        }) -join ','
}

$vars = @{
    SERENDIP_KUBECONFIG               = $kubeconfig
    SERENDIP_DB_PATH                  = $db
    SERENDIP_ADDR                     = $addr
    SERENDIP_OPENBAO_CREDENTIALS_PATH = $openbao
    SERENDIP_API_KEYS                 = $keys
}
$log = Join-Path $StateDir 'serendipd.log'
$out = Join-Path $StateDir 'serendipd.out.log'

Step "Starting serendipd on $addr (store $db)"
# Start-Process passes this shell's environment on; set the variables only
# for the launch, then put the old values back.
$saved = @{}
foreach ($k in $vars.Keys) {
    $saved[$k] = [Environment]::GetEnvironmentVariable($k, 'Process')
    [Environment]::SetEnvironmentVariable($k, $vars[$k], 'Process')
}
try {
    $proc = Start-Process -FilePath $bin -WorkingDirectory $StateDir -WindowStyle Hidden `
        -RedirectStandardError $log -RedirectStandardOutput $out -PassThru
} finally {
    foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
}
Set-Content -Path (Join-Path $StateDir 'serendipd.pid') -Value $proc.Id

# --- 4. Wait for the console -----------------------------------------------
$url = "http://localhost:$port/"
$deadline = (Get-Date).AddSeconds(30)
$up = $false
do {
    if ($proc.HasExited) { break }
    try {
        $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3
        if ($r.StatusCode -eq 200) { $up = $true; break }
    } catch { }
    Start-Sleep -Seconds 1
} while ((Get-Date) -lt $deadline)

if (-not $up) {
    Write-Host "--- last lines of $log ---"
    if (Test-Path $log) { Get-Content $log -Tail 20 }
    Fail "serendipd didn't answer on $url (exited: $($proc.HasExited))"
}

Write-Host ''
Write-Host "Console: $url  (use localhost, not 127.0.0.1)" -ForegroundColor Green
Write-Host "serendipd PID $($proc.Id), log $log"
Write-Host "Stop it: Stop-Process -Id $($proc.Id)"
