[CmdletBinding()]
param(
    [string]$BaseUrl = "https://link.zeroscan.st/zhp2pproxy",
    [string]$InstallDir = (Join-Path $env:USERPROFILE "zhp2pproxy"),
    [string]$ServiceName = "ZHP2PProxy",
    [ValidateSet("Prompt", "Apply", "Skip")]
    [string]$NodeConfigRepair = "Prompt"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Write-InstallLog {
    param([string]$Message)
    Write-Host "[zhp2pproxy-install] $Message"
}

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Run PowerShell as Administrator to install the Windows service."
    }
}

function Resolve-Architecture {
    $architecture = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) {
        $architecture = $env:PROCESSOR_ARCHITEW6432
    }
    switch ($architecture.ToUpperInvariant()) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default { throw "Unsupported Windows CPU architecture: $architecture" }
    }
}

function Write-Utf8NoBom {
    param(
        [string]$Path,
        [string]$Content
    )
    $utf8NoBom = New-Object System.Text.UTF8Encoding -ArgumentList $false
    [IO.File]::WriteAllText($Path, $Content, $utf8NoBom)
}

function Set-MinimalZHCashRpcFallback {
    param(
        [string]$NodeConfigPath,
        [string]$ProxyConfigPath
    )

    $marker = "# BEGIN ZHP2PProxy managed RPC fallback"
    $existingContent = ""
    if (Test-Path -LiteralPath $NodeConfigPath -PathType Leaf) {
        $existingContent = [IO.File]::ReadAllText($NodeConfigPath)
        if ($existingContent.Contains($marker)) {
            Write-InstallLog "Minimal RPC fallback already exists in $NodeConfigPath"
            return $false
        }
        $backupPath = "$NodeConfigPath.before-zhp2pproxy-$(Get-Date -Format 'yyyyMMddHHmmss').bak"
        Copy-Item -LiteralPath $NodeConfigPath -Destination $backupPath
        Write-InstallLog "Backed up existing node config to $backupPath"
    } else {
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $NodeConfigPath) | Out-Null
    }

    $preservedLines = @()
    if ($existingContent) {
        $preservedLines = $existingContent -split "`r?`n" | Where-Object {
            $_ -notmatch "^\s*(server|rpcuser|rpcpassword|rpcport|rpcbind|rpcallowip)\s*="
        }
    }
    $rpcPassword = [Guid]::NewGuid().ToString("N") + [Guid]::NewGuid().ToString("N")
    $managedLines = @(
        "",
        $marker,
        "# Local RPC only. Generated after a failed local-node connectivity check.",
        "server=1",
        "rpcuser=zhp2pproxy",
        "rpcpassword=$rpcPassword",
        "rpcport=3889",
        "rpcbind=127.0.0.1",
        "rpcallowip=127.0.0.1",
        "# END ZHP2PProxy managed RPC fallback",
        ""
    )
    Write-Utf8NoBom -Path $NodeConfigPath -Content (($preservedLines + $managedLines) -join [Environment]::NewLine)

    $proxyBackupPath = "$ProxyConfigPath.before-node-fallback-$(Get-Date -Format 'yyyyMMddHHmmss').bak"
    Copy-Item -LiteralPath $ProxyConfigPath -Destination $proxyBackupPath
    $proxyConfig = Get-Content -Raw -LiteralPath $ProxyConfigPath | ConvertFrom-Json
    $proxyConfig | Add-Member -NotePropertyName node_config_path -NotePropertyValue $NodeConfigPath -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_url -NotePropertyValue "" -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_host -NotePropertyValue "127.0.0.1" -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_port -NotePropertyValue 3889 -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_user -NotePropertyValue "" -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_pass -NotePropertyValue "" -Force
    $proxyConfig | Add-Member -NotePropertyName rpc_cookie_path -NotePropertyValue "" -Force
    $json = $proxyConfig | ConvertTo-Json -Depth 20
    Write-Utf8NoBom -Path $ProxyConfigPath -Content ($json + [Environment]::NewLine)
    Write-InstallLog "Wrote a loopback-only minimal RPC fallback to $NodeConfigPath"
    return $true
}

Assert-Administrator
$baseUri = [Uri]$BaseUrl
if ($baseUri.Scheme -ne "https") {
    throw "Download base URL must use HTTPS."
}

if ([Net.ServicePointManager]::SecurityProtocol -band [Net.SecurityProtocolType]::Tls12) {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
} else {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$architecture = Resolve-Architecture
$archiveName = "zhp2pproxy-windows-$architecture.zip"
$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("zhp2pproxy-install-" + [Guid]::NewGuid().ToString("N"))
$archivePath = Join-Path $tempDir $archiveName
$checksumsPath = Join-Path $tempDir "SHA256SUMS"
$unpackDir = Join-Path $tempDir "unpacked"
$packageDir = Join-Path $unpackDir "zhp2pproxy-windows-$architecture"

New-Item -ItemType Directory -Path $tempDir | Out-Null
try {
    Write-InstallLog "Downloading $archiveName"
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$archiveName" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/SHA256SUMS" -OutFile $checksumsPath

    $escapedArchiveName = [Regex]::Escape($archiveName)
    $checksumLine = Get-Content -LiteralPath $checksumsPath | Where-Object {
        $_ -match "^[0-9a-fA-F]{64}\s+\*?$escapedArchiveName$"
    } | Select-Object -First 1
    if (-not $checksumLine) {
        throw "Checksum for $archiveName is missing."
    }
    $expectedHash = ($checksumLine -split "\s+")[0].ToUpperInvariant()
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToUpperInvariant()
    if ($actualHash -ne $expectedHash) {
        throw "SHA-256 validation failed for $archiveName."
    }

    New-Item -ItemType Directory -Path $unpackDir | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $unpackDir
    $packageExe = Join-Path $packageDir "zhp2pproxy.exe"
    $packageConfig = Join-Path $packageDir "proxy.install.json"
    if (-not (Test-Path -LiteralPath $packageExe -PathType Leaf)) {
        throw "Archive does not contain zhp2pproxy.exe."
    }
    if (-not (Test-Path -LiteralPath $packageConfig -PathType Leaf)) {
        throw "Archive does not contain the safe config template."
    }

    $existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existingService -and $existingService.Status -ne "Stopped") {
        Write-InstallLog "Stopping existing service"
        Stop-Service -Name $ServiceName -Force
        (Get-Service -Name $ServiceName).WaitForStatus("Stopped", [TimeSpan]::FromSeconds(20))
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $installedExe = Join-Path $InstallDir "zhp2pproxy.exe"
    $installedConfig = Join-Path $InstallDir "proxy.json"
    if (Test-Path -LiteralPath $installedExe -PathType Leaf) {
        Copy-Item -LiteralPath $installedExe -Destination (Join-Path $InstallDir "zhp2pproxy.previous.exe") -Force
    }
    Copy-Item -LiteralPath $packageExe -Destination $installedExe -Force
    Copy-Item -LiteralPath (Join-Path $packageDir "README.md") -Destination (Join-Path $InstallDir "README.md") -Force
    Copy-Item -LiteralPath $packageConfig -Destination (Join-Path $InstallDir "proxy.example.json") -Force

    if (-not (Test-Path -LiteralPath $installedConfig -PathType Leaf)) {
        $config = Get-Content -Raw -LiteralPath $packageConfig | ConvertFrom-Json
        $nodeConfig = Join-Path $env:APPDATA "ZHCASH\zerohour.conf"
        if (Test-Path -LiteralPath $nodeConfig -PathType Leaf) {
            $config.node_config_path = $nodeConfig
            Write-InstallLog "Using ZHCASH config at $nodeConfig"
        }
        $json = $config | ConvertTo-Json -Depth 20
        Write-Utf8NoBom -Path $installedConfig -Content ($json + [Environment]::NewLine)
        Write-InstallLog "Created $installedConfig"
    } else {
        Write-InstallLog "Kept existing proxy.json, identity.key and peers.json"
    }

    & $installedExe -version
    if ($LASTEXITCODE -ne 0) {
        throw "The installed executable did not pass its version check."
    }

    # The service runs as LocalSystem and needs write access for identity.key and peers.json.
    & icacls.exe $InstallDir /grant "*S-1-5-18:(OI)(CI)F" /T /Q | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Could not grant the Windows service access to $InstallDir."
    }

    $binaryPath = ('"{0}" -config "{1}"' -f $installedExe, $installedConfig)
    if ($existingService) {
        & sc.exe config $ServiceName "binPath= $binaryPath" "start= delayed-auto" | Out-Null
    } else {
        & sc.exe create $ServiceName "binPath= $binaryPath" "start= delayed-auto" "DisplayName= ZHP2PProxy" | Out-Null
    }
    if ($LASTEXITCODE -ne 0) {
        throw "Could not create or update Windows service $ServiceName."
    }
    & sc.exe description $ServiceName "ZHCASH decentralized RPC and native P2P proxy" | Out-Null
    & sc.exe failure $ServiceName "reset= 86400" "actions= restart/5000/restart/5000/restart/5000" | Out-Null

    Start-Service -Name $ServiceName
    $service = Get-Service -Name $ServiceName
    $service.WaitForStatus("Running", [TimeSpan]::FromSeconds(30))
    if ($service.Status -ne "Running") {
        throw "Windows service $ServiceName did not become active."
    }

    $runtimeConfig = Get-Content -Raw -LiteralPath $installedConfig | ConvertFrom-Json
    $requestHeaders = @{}
    if (($runtimeConfig.PSObject.Properties.Name -contains "api_token") -and $runtimeConfig.api_token) {
        $requestHeaders["X-API-Token"] = $runtimeConfig.api_token
    }
    $localApiPort = 8081
    if (($runtimeConfig.PSObject.Properties.Name -contains "listen") -and ($runtimeConfig.listen -match ":([0-9]+)$")) {
        $localApiPort = [int]$Matches[1]
    }
    $localApiBase = "http://127.0.0.1:$localApiPort"

    $health = $null
    $lastHealthError = ""
    for ($attempt = 1; $attempt -le 15; $attempt++) {
        try {
            $health = Invoke-RestMethod -Method Get -Uri "$localApiBase/health" -Headers $requestHeaders -TimeoutSec 4
            if ($health.p2p_network.connected) {
                break
            }
        } catch {
            $lastHealthError = $_.Exception.Message
        }
        Start-Sleep -Seconds 2
    }

    $proxyRpcOk = $false
    $proxyBlockHeight = $null
    $lastRpcError = ""
    $rpcBody = '{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}'
    for ($attempt = 1; $attempt -le 10; $attempt++) {
        try {
            $rpcResponse = Invoke-RestMethod -Method Post -Uri "$localApiBase/" `
                -Headers $requestHeaders -ContentType "application/json" -Body $rpcBody -TimeoutSec 10
            if ($rpcResponse.PSObject.Properties.Name -contains "result") {
                $proxyRpcOk = $true
                $proxyBlockHeight = $rpcResponse.result
                break
            }
            if ($rpcResponse.PSObject.Properties.Name -contains "error") {
                $lastRpcError = $rpcResponse.error.message
            }
        } catch {
            $lastRpcError = $_.Exception.Message
        }
        Start-Sleep -Seconds 2
    }

    $nodeProcessNames = @("zerohourd", "zerohour-qt", "zhcashd", "zhcash-qt")
    $nodeProcess = Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $nodeProcessNames -contains $_.ProcessName
    } | Select-Object -First 1
    $defaultNodeConfig = Join-Path $env:APPDATA "ZHCASH\zerohour.conf"
    $localNodeEvidence = $nodeProcess -or (Test-Path -LiteralPath $defaultNodeConfig -PathType Leaf)
    $minimalConfigWritten = $false
    if ($health -and -not $health.local_node.connected -and $localNodeEvidence) {
        $fallbackMarkerExists = (Test-Path -LiteralPath $defaultNodeConfig -PathType Leaf) -and ([IO.File]::ReadAllText($defaultNodeConfig).Contains("# BEGIN ZHP2PProxy managed RPC fallback"))
        $applyNodeRepair = $NodeConfigRepair -eq "Apply" -and -not $fallbackMarkerExists
        if ($fallbackMarkerExists) {
            Write-InstallLog "Minimal RPC fallback already exists; no config prompt or rewrite is needed"
        } elseif ($NodeConfigRepair -eq "Prompt") {
            Write-Warning "A local ZHCASH node/config was found, but its RPC is unavailable."
            Write-Host "The fallback will back up and repair only RPC settings in:"
            Write-Host "  $defaultNodeConfig"
            Write-Host "  $installedConfig"
            Write-Host "It will bind RPC only to 127.0.0.1:3889 with a new random password."
            $answer = Read-Host "Apply this minimal local RPC fallback? [y/N]"
            $applyNodeRepair = $answer -match "^(y|yes|д|да)$"
        }
        if ($applyNodeRepair) {
            $minimalConfigWritten = Set-MinimalZHCashRpcFallback `
                -NodeConfigPath $defaultNodeConfig `
                -ProxyConfigPath $installedConfig
        } elseif (-not $fallbackMarkerExists) {
            Write-InstallLog "Skipped local node config repair; P2P-only operation remains available"
        }
    }

    Write-InstallLog "Service is running and starts automatically with Windows"
    Write-InstallLog "Installation directory: $InstallDir"
    Write-InstallLog "Config: $installedConfig"
    Write-InstallLog "Status: Get-Service $ServiceName"
    Write-InstallLog "Logs: Get-Content -Wait $(Join-Path $InstallDir 'zhp2pproxy.log')"
    Write-InstallLog "Local API: $localApiBase/info"
    Write-InstallLog "For a public relay, allow inbound TCP and UDP 10001 in Windows Firewall"
    if ($health -and $health.p2p_network.connected) {
        Write-InstallLog "P2P network connected: $($health.p2p_network.connected_peers) peer(s), peer ID $($health.p2p_network.peer_id)"
    } else {
        Write-Warning "The service is running, but no P2P peer was connected during the startup check. $lastHealthError"
    }
    if ($health -and $health.local_node.connected) {
        Write-InstallLog "Local ZHCASH RPC connected at block $($health.local_node.block_height)"
    } else {
        Write-InstallLog "Local ZHCASH RPC is not connected; this is allowed when zerohour.conf/local node is absent because RPC can use P2P peers"
    }
    if ($proxyRpcOk) {
        Write-InstallLog "Proxy RPC verified with getblockcount: $proxyBlockHeight"
    } else {
        Write-Warning "getblockcount did not complete through the proxy during the startup check: $lastRpcError"
    }
    if (-not $health) {
        Write-Warning "Health endpoint did not answer. Inspect $(Join-Path $InstallDir 'zhp2pproxy.log')"
    }
    if ($minimalConfigWritten) {
        Write-Warning "A minimal local RPC config was created. Restart the ZHCASH node, then run: Restart-Service $ServiceName"
    }
} finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force
    }
}
