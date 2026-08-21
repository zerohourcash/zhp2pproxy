[CmdletBinding()]
param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot "proxy.json"),
    [switch]$ShowWindow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

trap {
    $message = "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $($_.Exception.Message)$([Environment]::NewLine)"
    $errorLog = Join-Path $PSScriptRoot "zhp2pproxy-tray-error.log"
    [IO.File]::AppendAllText($errorLog, $message)
    try {
        Add-Type -AssemblyName System.Windows.Forms -ErrorAction SilentlyContinue
        [System.Windows.Forms.MessageBox]::Show($_.Exception.Message, "ZHP2PProxy indicator error") | Out-Null
    } catch {
    }
    exit 1
}

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class ZHP2PProxyTrayNative {
    [DllImport("user32.dll", SetLastError = true)]
    public static extern bool DestroyIcon(IntPtr handle);
}
"@

function New-StatusIcon {
    param([System.Drawing.Color]$Color)

    $bitmap = New-Object System.Drawing.Bitmap 32, 32
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.Clear([System.Drawing.Color]::Transparent)
    $brush = New-Object System.Drawing.SolidBrush $Color
    $pen = New-Object System.Drawing.Pen ([System.Drawing.Color]::White), 2
    $graphics.FillEllipse($brush, 3, 3, 26, 26)
    $graphics.DrawEllipse($pen, 4, 4, 24, 24)
    $handle = $bitmap.GetHicon()
    $icon = [System.Drawing.Icon]::FromHandle($handle).Clone()
    [ZHP2PProxyTrayNative]::DestroyIcon($handle) | Out-Null
    $pen.Dispose()
    $brush.Dispose()
    $graphics.Dispose()
    $bitmap.Dispose()
    return $icon
}

function Read-TrayConfig {
    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
        throw "Config not found: $ConfigPath"
    }
    $config = Get-Content -Raw -LiteralPath $ConfigPath | ConvertFrom-Json
    $port = 8081
    if (($config.PSObject.Properties.Name -contains "listen") -and ($config.listen -match ":([0-9]+)$")) {
        $port = [int]$Matches[1]
    }
    $headers = @{}
    if (($config.PSObject.Properties.Name -contains "api_token") -and $config.api_token) {
        $headers["X-API-Token"] = [string]$config.api_token
    }
    return [PSCustomObject]@{
        BaseUrl = "http://127.0.0.1:$port"
        Headers = $headers
    }
}

function Get-AutostartText {
    $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='ZHP2PProxy'" -ErrorAction SilentlyContinue
    if ($service -and $service.StartMode -ne "Disabled") {
        return "Windows service: $($service.State) / $($service.StartMode)"
    }
    if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
        $task = Get-ScheduledTask -TaskName "ZHP2PProxy-Startup" -ErrorAction SilentlyContinue
        if ($task) {
            return "Startup task: $($task.State)"
        }
    }
    return "Autostart: not detected"
}

function Get-ProxyStatus {
    try {
        $connection = Read-TrayConfig
        $health = Invoke-RestMethod -Method Get -Uri "$($connection.BaseUrl)/health" `
            -Headers $connection.Headers -TimeoutSec 3
        $rpcBody = '{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}'
        $rpc = Invoke-RestMethod -Method Post -Uri "$($connection.BaseUrl)/" `
            -Headers $connection.Headers -ContentType "application/json" -Body $rpcBody -TimeoutSec 5

        $p2pConnected = [bool]$health.p2p_network.connected
        $peerCount = [int]$health.p2p_network.connected_peers
        $localConnected = [bool]$health.local_node.connected
        $localBlock = if ($localConnected) { [string]$health.local_node.block_height } else { "not connected (optional)" }
        $rpcOK = $rpc -and ($rpc.PSObject.Properties.Name -contains "result")
        $rpcBlock = if ($rpcOK) { [string]$rpc.result } else { "failed" }

        if ($p2pConnected -and $rpcOK) {
            $level = "ok"
            $headline = "Connected"
        } elseif ($health.ok) {
            $level = "warning"
            $headline = "Running with limited connectivity"
        } else {
            $level = "warning"
            $headline = "Running, waiting for network"
        }
        return [PSCustomObject]@{
            Level = $level
            Headline = $headline
            Tooltip = "ZHP2PProxy: $headline; peers $peerCount"
            Details = @(
                "Status: $headline"
                "P2P network: $p2pConnected"
                "Connected peers: $peerCount"
                "Proxy RPC block: $rpcBlock"
                "Local node: $localBlock"
                (Get-AutostartText)
                "Checked: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
            ) -join [Environment]::NewLine
        }
    } catch {
        return [PSCustomObject]@{
            Level = "error"
            Headline = "Not reachable"
            Tooltip = "ZHP2PProxy: not reachable"
            Details = @(
                "Status: proxy is not reachable"
                (Get-AutostartText)
                "Error: $($_.Exception.Message)"
                "Checked: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
            ) -join [Environment]::NewLine
        }
    }
}

$createdNew = $false
$mutex = New-Object System.Threading.Mutex($true, "Local\ZHP2PProxyTray", [ref]$createdNew)
if (-not $createdNew) {
    exit 0
}

[System.Windows.Forms.Application]::EnableVisualStyles()
$script:greenIcon = New-StatusIcon -Color ([System.Drawing.Color]::FromArgb(38, 180, 92))
$script:yellowIcon = New-StatusIcon -Color ([System.Drawing.Color]::FromArgb(240, 174, 35))
$script:redIcon = New-StatusIcon -Color ([System.Drawing.Color]::FromArgb(220, 58, 58))
$script:lastLevel = ""
$script:lastStatus = $null

$script:notifyIcon = New-Object System.Windows.Forms.NotifyIcon
$script:notifyIcon.Icon = $script:yellowIcon
$script:notifyIcon.Text = "ZHP2PProxy: checking"
$script:notifyIcon.Visible = $true

$script:form = New-Object System.Windows.Forms.Form
$script:form.Text = "ZHP2PProxy status"
$script:form.Size = New-Object System.Drawing.Size 620, 360
$script:form.MinimumSize = New-Object System.Drawing.Size 560, 320
$script:form.StartPosition = "CenterScreen"
$script:form.BackColor = [System.Drawing.Color]::FromArgb(22, 24, 28)
$script:form.ForeColor = [System.Drawing.Color]::White
$script:form.ShowInTaskbar = $true

$title = New-Object System.Windows.Forms.Label
$title.Text = "ZHP2PProxy"
$title.Font = New-Object System.Drawing.Font "Segoe UI", 20, ([System.Drawing.FontStyle]::Bold)
$title.Location = New-Object System.Drawing.Point 24, 20
$title.AutoSize = $true
$script:form.Controls.Add($title)

$script:headlineLabel = New-Object System.Windows.Forms.Label
$script:headlineLabel.Text = "Checking..."
$script:headlineLabel.Font = New-Object System.Drawing.Font "Segoe UI", 12, ([System.Drawing.FontStyle]::Bold)
$script:headlineLabel.Location = New-Object System.Drawing.Point 28, 68
$script:headlineLabel.AutoSize = $true
$script:form.Controls.Add($script:headlineLabel)

$script:detailsBox = New-Object System.Windows.Forms.TextBox
$script:detailsBox.Location = New-Object System.Drawing.Point 28, 105
$script:detailsBox.Size = New-Object System.Drawing.Size 545, 150
$script:detailsBox.Anchor = "Top,Bottom,Left,Right"
$script:detailsBox.Multiline = $true
$script:detailsBox.ReadOnly = $true
$script:detailsBox.BorderStyle = "FixedSingle"
$script:detailsBox.BackColor = [System.Drawing.Color]::FromArgb(31, 34, 40)
$script:detailsBox.ForeColor = [System.Drawing.Color]::FromArgb(225, 230, 238)
$script:detailsBox.Font = New-Object System.Drawing.Font "Consolas", 10
$script:form.Controls.Add($script:detailsBox)

$refreshButton = New-Object System.Windows.Forms.Button
$refreshButton.Text = "Check now"
$refreshButton.Location = New-Object System.Drawing.Point 28, 270
$refreshButton.Size = New-Object System.Drawing.Size 100, 34
$refreshButton.Anchor = "Bottom,Left"
$script:form.Controls.Add($refreshButton)

$logButton = New-Object System.Windows.Forms.Button
$logButton.Text = "Open log"
$logButton.Location = New-Object System.Drawing.Point 138, 270
$logButton.Size = New-Object System.Drawing.Size 100, 34
$logButton.Anchor = "Bottom,Left"
$script:form.Controls.Add($logButton)

$restartButton = New-Object System.Windows.Forms.Button
$restartButton.Text = "Restart proxy"
$restartButton.Location = New-Object System.Drawing.Point 248, 270
$restartButton.Size = New-Object System.Drawing.Size 110, 34
$restartButton.Anchor = "Bottom,Left"
$script:form.Controls.Add($restartButton)

$stopButton = New-Object System.Windows.Forms.Button
$stopButton.Text = "Stop proxy"
$stopButton.Location = New-Object System.Drawing.Point 368, 270
$stopButton.Size = New-Object System.Drawing.Size 100, 34
$stopButton.Anchor = "Bottom,Right"
$script:form.Controls.Add($stopButton)

$hideButton = New-Object System.Windows.Forms.Button
$hideButton.Text = "Hide to tray"
$hideButton.Location = New-Object System.Drawing.Point 478, 270
$hideButton.Size = New-Object System.Drawing.Size 110, 34
$hideButton.Anchor = "Bottom,Right"
$script:form.Controls.Add($hideButton)

function Set-DisplayedStatus {
    param($Status, [bool]$ShowNotification)

    $script:lastStatus = $Status
    $tooltip = [string]$Status.Tooltip
    if ($tooltip.Length -gt 63) {
        $tooltip = $tooltip.Substring(0, 63)
    }
    $script:notifyIcon.Text = $tooltip
    $script:headlineLabel.Text = $Status.Headline
    $script:detailsBox.Text = $Status.Details
    switch ($Status.Level) {
        "ok" {
            $script:notifyIcon.Icon = $script:greenIcon
            $script:headlineLabel.ForeColor = [System.Drawing.Color]::FromArgb(56, 210, 116)
            $balloonIcon = [System.Windows.Forms.ToolTipIcon]::Info
        }
        "warning" {
            $script:notifyIcon.Icon = $script:yellowIcon
            $script:headlineLabel.ForeColor = [System.Drawing.Color]::FromArgb(245, 190, 65)
            $balloonIcon = [System.Windows.Forms.ToolTipIcon]::Warning
        }
        default {
            $script:notifyIcon.Icon = $script:redIcon
            $script:headlineLabel.ForeColor = [System.Drawing.Color]::FromArgb(245, 88, 88)
            $balloonIcon = [System.Windows.Forms.ToolTipIcon]::Error
        }
    }
    if ($ShowNotification -or $script:lastLevel -ne $Status.Level) {
        $script:notifyIcon.BalloonTipTitle = "ZHP2PProxy"
        $script:notifyIcon.BalloonTipText = $Status.Headline
        $script:notifyIcon.BalloonTipIcon = $balloonIcon
        $script:notifyIcon.ShowBalloonTip(4000)
    }
    $script:lastLevel = $Status.Level
}

function Update-DisplayedStatus {
    param([bool]$ShowNotification = $false)
    $status = Get-ProxyStatus
    Set-DisplayedStatus -Status $status -ShowNotification $ShowNotification
}

function Show-StatusWindow {
    Update-DisplayedStatus
    if (-not $script:form.Visible) {
        $script:form.Show()
    }
    if ($script:form.WindowState -eq [System.Windows.Forms.FormWindowState]::Minimized) {
        $script:form.WindowState = [System.Windows.Forms.FormWindowState]::Normal
    }
    $script:form.Activate()
}

function Open-ProxyLog {
    $logPath = Join-Path (Split-Path -Parent $ConfigPath) "zhp2pproxy.log"
    if (-not (Test-Path -LiteralPath $logPath -PathType Leaf)) {
        [System.Windows.Forms.MessageBox]::Show("The log has not been created yet: $logPath", "ZHP2PProxy") | Out-Null
        return
    }
    Start-Process -FilePath "notepad.exe" -ArgumentList ('"{0}"' -f $logPath)
}

function Invoke-ProxyControl {
    param([ValidateSet("Start", "Stop", "Restart")][string]$Action)

    if ($Action -eq "Stop") {
        $choice = [System.Windows.Forms.MessageBox]::Show(
            "Stop the ZHP2PProxy service now? The tray indicator will remain open and show it as offline.",
            "Stop ZHP2PProxy",
            [System.Windows.Forms.MessageBoxButtons]::YesNo,
            [System.Windows.Forms.MessageBoxIcon]::Warning
        )
        if ($choice -ne [System.Windows.Forms.DialogResult]::Yes) {
            return
        }
    }
    $controlScript = Join-Path $PSScriptRoot "zhp2pproxy-control.ps1"
    if (-not (Test-Path -LiteralPath $controlScript -PathType Leaf)) {
        [System.Windows.Forms.MessageBox]::Show("Control script not found: $controlScript", "ZHP2PProxy") | Out-Null
        return
    }
    $powerShellPath = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    $arguments = '-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}" -Action {1}' -f $controlScript, $Action
    try {
        $process = Start-Process -FilePath $powerShellPath -ArgumentList $arguments -Verb RunAs -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw "Control command exited with code $($process.ExitCode)."
        }
        Start-Sleep -Milliseconds 750
        Update-DisplayedStatus -ShowNotification $true
    } catch {
        [System.Windows.Forms.MessageBox]::Show($_.Exception.Message, "ZHP2PProxy control error") | Out-Null
    }
}

$menu = New-Object System.Windows.Forms.ContextMenuStrip
$statusMenuItem = $menu.Items.Add("Open status")
$refreshMenuItem = $menu.Items.Add("Check now")
$logMenuItem = $menu.Items.Add("Open log")
$folderMenuItem = $menu.Items.Add("Open folder")
$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator)) | Out-Null
$startMenuItem = $menu.Items.Add("Start proxy")
$restartMenuItem = $menu.Items.Add("Restart proxy")
$stopMenuItem = $menu.Items.Add("Stop proxy...")
$menu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator)) | Out-Null
$exitMenuItem = $menu.Items.Add("Exit indicator")
$script:notifyIcon.ContextMenuStrip = $menu

$statusMenuItem.Add_Click({ Show-StatusWindow })
$refreshMenuItem.Add_Click({ Update-DisplayedStatus -ShowNotification $true })
$logMenuItem.Add_Click({ Open-ProxyLog })
$folderMenuItem.Add_Click({ Start-Process -FilePath "explorer.exe" -ArgumentList ('"{0}"' -f (Split-Path -Parent $ConfigPath)) })
$startMenuItem.Add_Click({ Invoke-ProxyControl -Action "Start" })
$restartMenuItem.Add_Click({ Invoke-ProxyControl -Action "Restart" })
$stopMenuItem.Add_Click({ Invoke-ProxyControl -Action "Stop" })
$exitMenuItem.Add_Click({
    $script:notifyIcon.Visible = $false
    [System.Windows.Forms.Application]::Exit()
})
$script:notifyIcon.Add_MouseClick({
    param($sender, $eventArgs)
    if ($eventArgs.Button -eq [System.Windows.Forms.MouseButtons]::Left) {
        Show-StatusWindow
    }
})
$refreshButton.Add_Click({ Update-DisplayedStatus -ShowNotification $true })
$logButton.Add_Click({ Open-ProxyLog })
$restartButton.Add_Click({ Invoke-ProxyControl -Action "Restart" })
$stopButton.Add_Click({ Invoke-ProxyControl -Action "Stop" })
$hideButton.Add_Click({ $script:form.Hide() })
$script:form.Add_FormClosing({
    param($sender, $eventArgs)
    if ($eventArgs.CloseReason -eq [System.Windows.Forms.CloseReason]::UserClosing) {
        $eventArgs.Cancel = $true
        $script:form.Hide()
    }
})

$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 15000
$timer.Add_Tick({ Update-DisplayedStatus })
$timer.Start()

try {
    Update-DisplayedStatus -ShowNotification $true
    if ($ShowWindow) {
        Show-StatusWindow
    }
    [System.Windows.Forms.Application]::Run()
} finally {
    $timer.Stop()
    $timer.Dispose()
    $script:notifyIcon.Visible = $false
    $script:notifyIcon.Dispose()
    $script:form.Dispose()
    $script:greenIcon.Dispose()
    $script:yellowIcon.Dispose()
    $script:redIcon.Dispose()
    $mutex.ReleaseMutex()
    $mutex.Dispose()
}
