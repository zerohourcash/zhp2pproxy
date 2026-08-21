[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("Start", "Stop", "Restart")]
    [string]$Action,
    [string]$ServiceName = "ZHP2PProxy",
    [string]$TaskName = "ZHP2PProxy-Startup"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

trap {
    $message = "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') action=$Action $($_.Exception.Message)$([Environment]::NewLine)"
    [IO.File]::AppendAllText((Join-Path $PSScriptRoot "zhp2pproxy-control-error.log"), $message)
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Administrator rights are required to control ZHP2PProxy."
}

$service = Get-CimInstance -ClassName Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue
if ($service -and $service.StartMode -ne "Disabled") {
    switch ($Action) {
        "Start" {
            if ($service.State -ne "Running") {
                Start-Service -Name $ServiceName
            }
        }
        "Stop" {
            if ($service.State -ne "Stopped") {
                Stop-Service -Name $ServiceName -Force
            }
        }
        "Restart" {
            if ($service.State -eq "Running") {
                Restart-Service -Name $ServiceName -Force
            } else {
                Start-Service -Name $ServiceName
            }
        }
    }
    $current = Get-Service -Name $ServiceName
    $expected = if ($Action -eq "Stop") {
        [System.ServiceProcess.ServiceControllerStatus]::Stopped
    } else {
        [System.ServiceProcess.ServiceControllerStatus]::Running
    }
    $current.WaitForStatus($expected, [TimeSpan]::FromSeconds(30))
    if ($current.Status -ne $expected) {
        throw "Service did not reach $expected state. Current state: $($current.Status)"
    }
    Write-Output "ZHP2PProxy service: $($current.Status)"
    exit 0
}

if (-not (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue)) {
    throw "Neither an active Windows service nor Scheduled Task support was found."
}
$task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if (-not $task) {
    throw "Neither an active Windows service nor startup task $TaskName was found."
}

switch ($Action) {
    "Start" {
        if ($task.State -ne "Running") {
            Start-ScheduledTask -TaskName $TaskName
        }
    }
    "Stop" {
        if ($task.State -eq "Running") {
            Stop-ScheduledTask -TaskName $TaskName
        }
    }
    "Restart" {
        if ($task.State -eq "Running") {
            Stop-ScheduledTask -TaskName $TaskName
            Start-Sleep -Seconds 1
        }
        Start-ScheduledTask -TaskName $TaskName
    }
}

Start-Sleep -Seconds 2
$current = Get-ScheduledTask -TaskName $TaskName
if ($Action -eq "Stop") {
    if ($current.State -eq "Running") {
        throw "Startup task is still running after the stop request."
    }
} elseif ($current.State -ne "Running") {
    $taskInfo = Get-ScheduledTaskInfo -TaskName $TaskName -ErrorAction SilentlyContinue
    $lastTaskResult = if ($taskInfo) { $taskInfo.LastTaskResult } else { "unavailable" }
    throw "Startup task did not remain running. State=$($current.State); LastTaskResult=$lastTaskResult"
}
Write-Output "ZHP2PProxy startup task: $($current.State)"
