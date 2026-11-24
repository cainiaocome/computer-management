$ErrorActionPreference = 'Continue'
$log = 'C:\OEM\bootstrap.log'
function Log($m) { $m | Tee-Object -FilePath $log -Append }

Log "installing OpenSSH.Server capability"
for ($i = 1; $i -le 5; $i++) {
    try {
        Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 -ErrorAction Stop | Out-Null
        Log "capability installed"
        break
    } catch {
        Log "attempt $i failed: $_"
        Start-Sleep -Seconds 20
    }
}

Set-Service -Name sshd -StartupType Automatic
Start-Service sshd
Log "sshd: $((Get-Service sshd).Status)"

if (-not (Get-NetFirewallRule -Name 'fleet-e2e-ssh' -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name 'fleet-e2e-ssh' -DisplayName 'fleet e2e ssh' `
        -Protocol TCP -LocalPort 22 -Action Allow -Direction Inbound | Out-Null
}

# Admin users authenticate against this file, not %USERPROFILE%\.ssh\authorized_keys.
$keyFile = 'C:\OEM\authorized_keys'
if (Test-Path $keyFile) {
    $dir = "$env:ProgramData\ssh"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $dest = Join-Path $dir 'administrators_authorized_keys'
    Copy-Item -Force $keyFile $dest
    icacls $dest /inheritance:r /grant 'Administrators:F' /grant 'SYSTEM:F' | Out-Null
    Log "authorized key installed at $dest"
} else {
    Log "WARNING: no authorized_keys found in C:\OEM"
}
