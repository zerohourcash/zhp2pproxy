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

function Get-WindowsServiceResultMessage {
    param([int]$Code)

    $messages = @{
        0 = "success"
        1 = "not supported"
        2 = "access denied"
        3 = "dependent services are running"
        4 = "invalid service control"
        5 = "service cannot accept control"
        6 = "service is not active"
        7 = "service request timed out"
        8 = "unknown failure"
        9 = "executable path was not found"
        10 = "service is already running"
        11 = "service database is locked"
        12 = "a service dependency was deleted"
        13 = "a service dependency failed"
        14 = "service is disabled"
        15 = "service logon failed"
        16 = "service is marked for deletion; reboot Windows and retry"
        17 = "service has no execution thread"
        18 = "circular service dependency"
        19 = "duplicate service name"
        20 = "invalid service name"
        21 = "invalid service parameter"
        22 = "invalid service account"
        23 = "service already exists"
        24 = "service is already paused"
    }
    if ($messages.ContainsKey($Code)) {
        return $messages[$Code]
    }
    return "unrecognized Windows service error"
}

function Install-ProxyWindowsService {
    param(
        [string]$Name,
        [string]$BinaryPath
    )

    if ($Name -notmatch "^[A-Za-z0-9_.-]+$") {
        throw "Invalid Windows service name: $Name"
    }
    $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$Name'" -ErrorAction SilentlyContinue
    if ($service) {
        $result = Invoke-CimMethod -InputObject $service -MethodName Change -Arguments @{
            DisplayName = "ZHP2PProxy"
            PathName = $BinaryPath
            StartMode = "Automatic"
        }
        if ([int]$result.ReturnValue -ne 0) {
            $description = Get-WindowsServiceResultMessage -Code ([int]$result.ReturnValue)
            throw "Win32_Service.Change failed with code $($result.ReturnValue): $description. Binary path: $BinaryPath"
        }
    } else {
        try {
            New-Service -Name $Name -BinaryPathName $BinaryPath `
                -DisplayName "ZHP2PProxy" -Description "ZHCASH decentralized RPC and native P2P proxy" `
                -StartupType Automatic -ErrorAction Stop | Out-Null
        } catch {
            throw "New-Service failed: $($_.Exception.Message). Binary path: $BinaryPath"
        }
    }

    # Automatic startup is the essential setting. DelayedAutoStart makes boot less
    # fragile when networking is not ready yet, and does not depend on sc.exe parsing.
    $serviceRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$Name"
    Set-Service -Name $Name -StartupType Automatic
    New-ItemProperty -LiteralPath $serviceRegistryPath -Name DelayedAutostart `
        -PropertyType DWord -Value 1 -Force | Out-Null
    New-ItemProperty -LiteralPath $serviceRegistryPath -Name Description `
        -PropertyType String -Value "ZHCASH decentralized RPC and native P2P proxy" -Force | Out-Null

    $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$Name'" -ErrorAction Stop
    $delayedAutoStart = (Get-ItemProperty -LiteralPath $serviceRegistryPath -Name DelayedAutostart).DelayedAutostart
    if ($service.StartMode -ne "Auto" -or $service.PathName -ne $BinaryPath -or $delayedAutoStart -ne 1) {
        throw "Windows service verification failed. StartMode=$($service.StartMode); DelayedAutoStart=$delayedAutoStart; PathName=$($service.PathName)"
    }
}

function Install-ProxyStartupTask {
    param(
        [string]$TaskName,
        [string]$Executable,
        [string]$ConfigPath
    )

    $action = New-ScheduledTaskAction -Execute $Executable -Argument ('-config "{0}"' -f $ConfigPath)
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable `
        -RestartCount 5 -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Description "ZHP2PProxy startup fallback" `
        -Force -ErrorAction Stop | Out-Null

    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop
    $hasBootTrigger = $task.Triggers | Where-Object { $_.CimClass.CimClassName -eq "MSFT_TaskBootTrigger" }
    $hasExpectedAction = $task.Actions | Where-Object {
        $_.Execute -eq $Executable -and $_.Arguments -eq ('-config "{0}"' -f $ConfigPath)
    }
    if (-not $task -or -not $hasBootTrigger -or -not $hasExpectedAction -or $task.Principal.UserId -notmatch "(?i)(SYSTEM|S-1-5-18)") {
        throw "Scheduled Task $TaskName verification failed: expected LocalSystem, AtStartup and the installed executable."
    }
}

function Install-ProxyTrayStartup {
    param(
        [string]$TrayScript,
        [string]$ConfigPath
    )

    $powerShellPath = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    if (-not (Test-Path -LiteralPath $powerShellPath -PathType Leaf)) {
        throw "Windows PowerShell was not found at $powerShellPath"
    }
    $startupDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Startup)
    if (-not $startupDirectory) {
        throw "The current user's Windows Startup folder could not be resolved."
    }
    New-Item -ItemType Directory -Force -Path $startupDirectory | Out-Null
    $shortcutPath = Join-Path $startupDirectory "ZHP2PProxy Status.lnk"
    $shortcutArguments = '-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}" -ConfigPath "{1}"' -f $TrayScript, $ConfigPath
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $powerShellPath
    $shortcut.Arguments = $shortcutArguments
    $shortcut.WorkingDirectory = Split-Path -Parent $TrayScript
    $shortcut.Description = "Visible ZHP2PProxy network status and controls"
    $shortcut.Save()

    $verifiedShortcut = $shell.CreateShortcut($shortcutPath)
    if (-not (Test-Path -LiteralPath $shortcutPath -PathType Leaf) -or
        $verifiedShortcut.TargetPath -ne $powerShellPath -or
        $verifiedShortcut.Arguments -ne $shortcutArguments) {
        throw "Tray indicator autostart shortcut verification failed: $shortcutPath"
    }
    return $shortcutPath
}

function Stop-ExistingProxyTray {
    param([string]$TrayScript)

    $trayProcesses = Get-CimInstance -ClassName Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        ($_.Name -eq "powershell.exe" -or $_.Name -eq "pwsh.exe") -and
        $_.CommandLine -and $_.CommandLine.IndexOf($TrayScript, [StringComparison]::OrdinalIgnoreCase) -ge 0
    }
    foreach ($process in $trayProcesses) {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
    if ($trayProcesses) {
        Start-Sleep -Milliseconds 500
    }
}

function Get-ProxyStartupDiagnostics {
    param(
        [string]$Mode,
        [string]$Name,
        [string]$LogPath
    )

    $details = @()
    if ($Mode -eq "service") {
        $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$Name'" -ErrorAction SilentlyContinue
        if ($service) {
            $details += "service state=$($service.State), startMode=$($service.StartMode), exitCode=$($service.ExitCode), serviceExitCode=$($service.ServiceSpecificExitCode)"
        } else {
            $details += "service is not registered"
        }
    } else {
        $task = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
        $taskInfo = Get-ScheduledTaskInfo -TaskName $Name -ErrorAction SilentlyContinue
        if ($task) {
            $lastTaskResult = if ($taskInfo) { $taskInfo.LastTaskResult } else { "unavailable" }
            $details += "scheduled task state=$($task.State), lastResult=$lastTaskResult"
        } else {
            $details += "scheduled task is not registered"
        }
    }
    if (Test-Path -LiteralPath $LogPath -PathType Leaf) {
        $tail = (Get-Content -LiteralPath $LogPath -Tail 25 -ErrorAction SilentlyContinue) -join [Environment]::NewLine
        if ($tail) {
            $details += "last proxy log lines:$([Environment]::NewLine)$tail"
        }
    }
    return ($details -join [Environment]::NewLine)
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
if ($ServiceName -ne "ZHP2PProxy") {
    throw "This binary uses the fixed Windows service name ZHP2PProxy; custom ServiceName values are not supported."
}
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
    $packageTray = Join-Path $packageDir "zhp2pproxy-tray.ps1"
    $packageControl = Join-Path $packageDir "zhp2pproxy-control.ps1"
    if (-not (Test-Path -LiteralPath $packageExe -PathType Leaf)) {
        throw "Archive does not contain zhp2pproxy.exe."
    }
    if (-not (Test-Path -LiteralPath $packageConfig -PathType Leaf)) {
        throw "Archive does not contain the safe config template."
    }
    if (-not (Test-Path -LiteralPath $packageTray -PathType Leaf) -or
        -not (Test-Path -LiteralPath $packageControl -PathType Leaf)) {
        throw "Archive does not contain the Windows status/control scripts."
    }

    $startupTaskName = "$ServiceName-Startup"
    $taskSchedulerAvailable = [bool](Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue)
    $existingTask = if ($taskSchedulerAvailable) {
        Get-ScheduledTask -TaskName $startupTaskName -ErrorAction SilentlyContinue
    } else {
        $null
    }
    if ($existingTask -and $existingTask.State -eq "Running") {
        Write-InstallLog "Stopping existing startup task"
        Stop-ScheduledTask -TaskName $startupTaskName -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
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
    $installedTray = Join-Path $InstallDir "zhp2pproxy-tray.ps1"
    $installedControl = Join-Path $InstallDir "zhp2pproxy-control.ps1"
    Stop-ExistingProxyTray -TrayScript $installedTray
    if (Test-Path -LiteralPath $installedExe -PathType Leaf) {
        Copy-Item -LiteralPath $installedExe -Destination (Join-Path $InstallDir "zhp2pproxy.previous.exe") -Force
    }
    Copy-Item -LiteralPath $packageExe -Destination $installedExe -Force
    Copy-Item -LiteralPath (Join-Path $packageDir "README.md") -Destination (Join-Path $InstallDir "README.md") -Force
    Copy-Item -LiteralPath $packageConfig -Destination (Join-Path $InstallDir "proxy.example.json") -Force
    Copy-Item -LiteralPath $packageTray -Destination $installedTray -Force
    Copy-Item -LiteralPath $packageControl -Destination $installedControl -Force

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
    $startupMode = "service"
    $startupName = $ServiceName
    try {
        Install-ProxyWindowsService -Name $ServiceName -BinaryPath $binaryPath

        # Recovery settings are useful but not required for registration. Preserve
        # their exact output when Windows rejects a particular recovery option.
        $recoveryOutput = & sc.exe failure $ServiceName "reset= 86400" "actions= restart/5000/restart/5000/restart/5000" 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "Service recovery policy was not applied (sc.exe exit $LASTEXITCODE): $($recoveryOutput -join ' ')"
        }

        if ($taskSchedulerAvailable -and (Get-ScheduledTask -TaskName $startupTaskName -ErrorAction SilentlyContinue)) {
            Unregister-ScheduledTask -TaskName $startupTaskName -Confirm:$false
        }
        Start-Service -Name $ServiceName -ErrorAction Stop
        $service = Get-Service -Name $ServiceName
        $service.WaitForStatus("Running", [TimeSpan]::FromSeconds(30))
        if ($service.Status -ne "Running") {
            throw "Windows service $ServiceName did not become active."
        }
    } catch {
        $serviceInstallError = $_.Exception.Message
        Write-Warning "Windows service registration/start failed: $serviceInstallError"
        Write-InstallLog "Installing an AtStartup Scheduled Task fallback under LocalSystem"
        if (-not $taskSchedulerAvailable) {
            throw "Service setup failed: $serviceInstallError$([Environment]::NewLine)Scheduled Task cmdlets are unavailable on this Windows installation."
        }
        $partialService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($partialService) {
            if ($partialService.Status -ne "Stopped") {
                Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
            }
            Set-Service -Name $ServiceName -StartupType Disabled -ErrorAction SilentlyContinue
        }
        try {
            Install-ProxyStartupTask -TaskName $startupTaskName -Executable $installedExe -ConfigPath $installedConfig
            Start-ScheduledTask -TaskName $startupTaskName -ErrorAction Stop
            $startupMode = "task"
            $startupName = $startupTaskName
            Start-Sleep -Seconds 2
        } catch {
            throw "Service setup failed: $serviceInstallError$([Environment]::NewLine)Scheduled Task fallback also failed: $($_.Exception.Message)"
        }
    }

    $proxyLogPath = Join-Path $InstallDir "zhp2pproxy.log"
    try {
        $probe = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:8081/health" -TimeoutSec 2
    } catch {
        $probe = $null
    }
    if (-not $probe) {
        Start-Sleep -Seconds 2
        $process = Get-Process -Name "zhp2pproxy" -ErrorAction SilentlyContinue
        if (-not $process) {
            $diagnostics = Get-ProxyStartupDiagnostics -Mode $startupMode -Name $startupName -LogPath $proxyLogPath
            throw "Autostart was registered, but zhp2pproxy exited before its health endpoint became available.$([Environment]::NewLine)$diagnostics"
        }
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

    if ($startupMode -eq "service") {
        Write-InstallLog "Windows service is running with Automatic (Delayed Start)"
        Write-InstallLog "Autostart verified: Win32_Service StartMode=Auto and DelayedAutostart=1"
    } else {
        Write-InstallLog "Scheduled Task fallback is running under LocalSystem and starts at Windows boot"
        Write-InstallLog "Autostart verified: $startupTaskName has an AtStartup trigger"
    }
    Write-InstallLog "Installation directory: $InstallDir"
    Write-InstallLog "Config: $installedConfig"
    if ($startupMode -eq "service") {
        Write-InstallLog "Status: Get-Service $ServiceName"
    } else {
        Write-InstallLog "Status: Get-ScheduledTask '$startupTaskName'; Get-ScheduledTaskInfo '$startupTaskName'"
    }
    Write-InstallLog "Logs: Get-Content -Wait $(Join-Path $InstallDir 'zhp2pproxy.log')"
    Write-InstallLog "Local API: $localApiBase/info"
    Write-InstallLog "For a public relay, allow inbound TCP and UDP 10001 in Windows Firewall"
    if ($health -and $health.p2p_network.connected) {
        Write-InstallLog "P2P network connected: $($health.p2p_network.connected_peers) peer(s), peer ID $($health.p2p_network.peer_id)"
    } else {
        Write-Warning "ZHP2PProxy is running, but no P2P peer was connected during the startup check. $lastHealthError"
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
        if ($startupMode -eq "service") {
            Write-Warning "A minimal local RPC config was created. Restart the ZHCASH node, then run: Restart-Service $ServiceName"
        } else {
            Write-Warning "A minimal local RPC config was created. Restart the ZHCASH node, then run: Stop-ScheduledTask '$startupTaskName'; Start-ScheduledTask '$startupTaskName'"
        }
    }

    $trayShortcut = Install-ProxyTrayStartup -TrayScript $installedTray -ConfigPath $installedConfig
    $powerShellPath = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    $trayArguments = '-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}" -ConfigPath "{1}" -ShowWindow' -f $installedTray, $installedConfig
    Start-Process -FilePath $powerShellPath -ArgumentList $trayArguments
    Start-Sleep -Seconds 2
    $trayProcess = Get-CimInstance -ClassName Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        ($_.Name -eq "powershell.exe" -or $_.Name -eq "pwsh.exe") -and
        $_.CommandLine -and $_.CommandLine.IndexOf($installedTray, [StringComparison]::OrdinalIgnoreCase) -ge 0
    } | Select-Object -First 1
    if (-not $trayProcess) {
        $trayErrorLog = Join-Path $InstallDir "zhp2pproxy-tray-error.log"
        $trayError = if (Test-Path -LiteralPath $trayErrorLog -PathType Leaf) {
            (Get-Content -LiteralPath $trayErrorLog -Tail 10) -join [Environment]::NewLine
        } else {
            "no tray error log was created"
        }
        throw "The visible tray indicator did not remain running. $trayError"
    }
    Write-InstallLog "Visible tray indicator is running and opens automatically at user logon"
    Write-InstallLog "Tray autostart: $trayShortcut"
} finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force
    }
}
