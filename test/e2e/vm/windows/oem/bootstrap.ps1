# Runs once at the end of the unattended Windows install (from install.bat).
#
# Goal: make the guest reachable over SSH so the host can drive the real test
# suite. We deliberately do NOT use Add-WindowsCapability: it pulls the
# OpenSSH.Server Feature-on-Demand from Windows Update and hangs for a long time
# in a fresh VM. Instead the host drops OpenSSH-Win64.zip into C:\OEM and we
# install it offline, which is fast and deterministic.
$ErrorActionPreference = 'Continue'
$log = 'C:\OEM\bootstrap.log'
function Log($m) { "$(Get-Date -Format o) $m" | Out-File -Append -Encoding utf8 $log }

Log "bootstrap start"

# 1. Authorised key first: everything else is useless if we cannot log in.
$keyFile = 'C:\OEM\authorized_keys'
if (Test-Path $keyFile) {
    $dir = "$env:ProgramData\ssh"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $dest = Join-Path $dir 'administrators_authorized_keys'
    Copy-Item -Force $keyFile $dest
    # OpenSSH refuses key files that other users can write.
    icacls $dest /inheritance:r /grant 'Administrators:F' /grant 'SYSTEM:F' | Out-Null
    Log "authorized key installed at $dest"
} else {
    Log "WARNING: no authorized_keys in C:\OEM"
}

# 2. Offline OpenSSH install.
$zip = 'C:\OEM\OpenSSH-Win64.zip'
$target = 'C:\Program Files\OpenSSH'
if (Test-Path $zip) {
    try {
        New-Item -ItemType Directory -Force -Path $target | Out-Null
        Expand-Archive -Path $zip -DestinationPath 'C:\OEM\x' -Force
        Copy-Item 'C:\OEM\x\OpenSSH-Win64\*' $target -Recurse -Force
        & "$target\install-sshd.ps1" *>> $log
        Log "openSSH installed from local zip"
    } catch {
        Log "offline install failed: $_"
    }
} else {
    Log "no OpenSSH zip staged; falling back to the capability (slow)"
    try { Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 -ErrorAction Stop | Out-Null } catch { Log "capability failed: $_" }
}

try {
    Set-Service -Name sshd -StartupType Automatic
    Start-Service sshd
    Log "sshd: $((Get-Service sshd).Status)"
} catch {
    Log "could not start sshd: $_"
}

if (-not (Get-NetFirewallRule -Name 'fleet-e2e-ssh' -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name 'fleet-e2e-ssh' -DisplayName 'fleet e2e ssh' `
        -Protocol TCP -LocalPort 22 -Action Allow -Direction Inbound | Out-Null
}

Log "bootstrap done"
