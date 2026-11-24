<#
.SYNOPSIS
    Install the fleet agent as a Windows service and enrol it.

.EXAMPLE
    .\install-windows.ps1 -Binary .\fleet-agent.exe -Server https://fleet.example.com -Token abc123

.NOTES
    Run from an elevated PowerShell prompt. The agent runs as LocalSystem so it
    can write HKLM/browser policy, manage packages and services.
#>
param(
    [string]$Binary = ".\fleet-agent.exe",
    [string]$Server = "",
    [string]$Token = "",
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$InstallDir = Join-Path $env:ProgramFiles "Fleet"
$ServiceName = "FleetAgent"

if ($Uninstall) {
    if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
        sc.exe delete $ServiceName | Out-Null
        Write-Host "service removed"
    }
    Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
    return
}

if (-not ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "must run from an elevated PowerShell prompt"
}
if (-not (Test-Path $Binary)) {
    throw "agent binary not found: $Binary"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$dest = Join-Path $InstallDir "fleet-agent.exe"
Copy-Item -Force $Binary $dest

# Defender: add an exclusion through the supported API if you need one. Never
# edit the registry directly: Tamper Protection blocks that and may re-enable
# the exclusion. Left commented out on purpose - most deployments need none.
# Add-MpPreference -ExclusionPath $InstallDir

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Stop-Service -Name $ServiceName -Force
    sc.exe delete $ServiceName | Out-Null
    Start-Sleep -Seconds 1
}

New-Service -Name $ServiceName `
    -BinaryPathName "`"$dest`" run" `
    -DisplayName "Fleet Device Agent" `
    -Description "Converges this machine to centrally managed policy." `
    -StartupType Automatic | Out-Null

# Restart on failure, forever.
sc.exe failure $ServiceName reset= 86400 actions= restart/30000/restart/30000/restart/30000 | Out-Null
Start-Service -Name $ServiceName

if ($Server -and $Token) {
    & $dest enroll -server $Server -token $Token
}

Write-Host "installed to $InstallDir and enrolled."
Write-Host "REMINDER: set the default browser by hand in Settings > Apps > Default apps."
Write-Host "          UCPD.sys denies programmatic changes, including from SYSTEM."
