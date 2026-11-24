# Real end-to-end suite for the fleet agent, executed INSIDE a Windows guest.
#
# Run as an elevated user (the VM harness does this over SSH). Every assertion
# below touches the real operating system: the real registry, real local
# accounts, real Program Files, the real service manager.
#
#   powershell -ExecutionPolicy Bypass -File verify.ps1 `
#       -Server http://host.lan:8080 -AdminToken <token> -Agent Z:\fleet-agent.exe
#
# Exits non-zero if any check fails and writes C:\fleet-e2e-results.json.

param(
    [string]$Server = "http://host.lan:8080",
    [string]$AdminToken = "e2e-vm-admin-token",
    [string]$Agent = "Z:\fleet-agent.exe",
    [string]$Installer = "Z:\install-windows.ps1",
    [string]$Group = "e2e-vm",
    [string]$StdUser = "fleetstd",
    [string]$AdmUser = "fleetadm",
    [string]$UserPassword = "E2e-Passw0rd!x",
    [string]$CfgPath = "$env:ProgramData\fleet\agent.json",
    [string]$FirefoxPolicy = "C:\Program Files\Mozilla Firefox\distribution\policies.json"
)

$ErrorActionPreference = 'Stop'
$script:results = @()
$script:failed = 0

function Check([string]$Name, [bool]$Ok, [string]$Detail = "") {
    $script:results += [pscustomobject]@{ name = $Name; ok = $Ok; detail = $Detail }
    if ($Ok) {
        Write-Host "  PASS  $Name" -ForegroundColor Green
    } else {
        Write-Host "  FAIL  $Name" -ForegroundColor Red
        if ($Detail) { Write-Host "        $Detail" -ForegroundColor DarkGray }
        $script:failed++
    }
}

function Section([string]$Name) { Write-Host "`n--- $Name" -ForegroundColor Cyan }

function Api([string]$Method, [string]$Path, $Body = $null) {
    $headers = @{ Authorization = "Bearer $AdminToken" }
    $uri = "$Server$Path"
    if ($null -ne $Body) {
        Invoke-RestMethod -Method $Method -Uri $uri -Headers $headers `
            -ContentType 'application/json' -Body ($Body | ConvertTo-Json -Depth 12)
    } else {
        Invoke-RestMethod -Method $Method -Uri $uri -Headers $headers
    }
}

function ReportStatus($reports, [string]$Id) {
    $r = $reports | Where-Object { $_.id -eq $Id }
    if ($null -eq $r) { return '<missing>' }
    return $r.status
}

# Native runs a command line through cmd.exe with stderr merged, and never
# throws. With $ErrorActionPreference = 'Stop', a native command that writes to
# stderr (e.g. `net user missing /delete`) becomes a terminating error, which
# makes a test suite fail for the wrong reason.
function Native([string]$line) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $out = cmd /c "$line 2>&1"
    $code = $LASTEXITCODE
    $ErrorActionPreference = $prev
    return [pscustomobject]@{ Output = (($out | Out-String).Trim()); Code = $code }
}

function UserChoice {
    $r = Native 'reg query "HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice" /v ProgId'
    return ("exit=$($r.Code) " + $r.Output)
}

function EnsureUser([string]$Name, [bool]$Admin) {
    Native "net user $Name /delete" | Out-Null
    $add = Native "net user $Name $UserPassword /add"
    if ($add.Code -ne 0) { return $false }
    if ($Admin) {
        Native "net localgroup Administrators $Name /add" | Out-Null
    } else {
        Native "net localgroup Administrators $Name /delete" | Out-Null
    }
    return $true
}

# --- 0. preconditions ------------------------------------------------------
Section "preconditions"
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
Check "session is elevated" $isAdmin "SSH logons of local admins need LocalAccountTokenFilterPolicy=1"
Check "agent binary staged" (Test-Path $Agent) $Agent
Check "installer staged" (Test-Path $Installer) $Installer

$os = Get-CimInstance Win32_OperatingSystem
Write-Host "  guest: $($os.Caption) build $($os.BuildNumber)" -ForegroundColor DarkGray

try { $health = Invoke-RestMethod "$Server/healthz" -TimeoutSec 10 } catch {
    Check "server reachable at $Server" $false "$_"
    exit 1
}
Check "server reachable at $Server" ($health.status -eq 'ok') "$Server"

# UCPD is what makes programmatic default-browser changes impossible. Its
# presence is informational: consumer Windows builds ship it, some do not.
$ucpd = Get-Service -Name UCPD -ErrorAction SilentlyContinue
Write-Host "  UCPD (UserChoice Protection Driver): $(if ($ucpd) { $ucpd.Status } else { 'not present' })" -ForegroundColor DarkGray

# --- 1. clean slate --------------------------------------------------------
Section "clean slate"
if (Test-Path $FirefoxPolicy) { Remove-Item -Force $FirefoxPolicy }
if (Test-Path $CfgPath) { Remove-Item -Force $CfgPath }
EnsureUser $StdUser $false | Out-Null
EnsureUser $AdmUser $true | Out-Null
Check "standard user can be created" ((Native "net user $StdUser").Output -match $StdUser)
Check "admin user can be created" ((Native "net user $AdmUser").Output -match $AdmUser)

# --- 2. enrolment ----------------------------------------------------------
Section "enrolment"
$tok = Api POST "/api/v1/admin/enrollment-tokens" @{ label = "e2e-windows"; ttl_seconds = 900; max_uses = 1 }
& $Agent enroll -config $CfgPath -server $Server -token $tok.token -log-level error
Check "enroll exits 0" ($LASTEXITCODE -eq 0) "exit $LASTEXITCODE"
Check "config written" (Test-Path $CfgPath) $CfgPath

$cfg = Get-Content $CfgPath -Raw | ConvertFrom-Json
Check "device id assigned" ([bool]$cfg.device_id) "$($cfg.device_id)"
Check "signing key pinned" ([bool]$cfg.signing_key)

# A replayed token must be refused, otherwise enrolment is not single use.
$code = 0
try {
    & $Agent enroll -config "$CfgPath.replay" -server $Server -token $tok.token -log-level error | Out-Null
    $code = 0
} catch { $code = 1 }
Check "replayed enrolment token is rejected" ($code -ne 0 -or -not (Test-Path "$CfgPath.replay"))

# --- 3. policy -------------------------------------------------------------
Section "policy assignment"
$policy = @{
    policies = @(
        @{ id = 'firefox'; type = 'firefox.policy'; class = 'enforce'; enabled = $true; config = @{
                homepage             = 'https://example.com'
                block_other_extensions = $true
                extensions           = @(@{ id = 'uBlock0@raymondhill.net'
                                             url = 'https://addons.mozilla.org/firefox/downloads/latest/ublock-origin/latest.xpi' })
            }
        }
        @{ id = 'std-user'; type = 'user.account'; class = 'enforce'; enabled = $true
           config = @{ username = $StdUser; ensure_standard = $true } }
        @{ id = 'adm-user'; type = 'user.account'; class = 'enforce'; enabled = $true
           config = @{ username = $AdmUser; ensure_standard = $true } }
        @{ id = 'browser'; type = 'browser.default'; class = 'nudge'; enabled = $true
           config = @{ browser = 'firefox' } }
        @{ id = 'inventory'; type = 'inventory'; class = 'report_only'; enabled = $true; config = @{} }
    )
}
Api PUT "/api/v1/admin/groups/$Group" $policy | Out-Null
Api PUT "/api/v1/admin/devices/$($cfg.device_id)" @{ group = $Group } | Out-Null
Check "group policy accepted" $true $Group

# --- 4. apply --------------------------------------------------------------
Section "apply"
$choiceBefore = UserChoice
$stdoutFile = "$env:TEMP\fleet-once.json"
& $Agent once -config $CfgPath -json -log-level error 1> $stdoutFile 2> "$env:TEMP\fleet-once.err"
$rc = $LASTEXITCODE
Check "apply exits 0" ($rc -eq 0) (Get-Content "$env:TEMP\fleet-once.err" -Raw)
$reports = Get-Content $stdoutFile -Raw | ConvertFrom-Json
Check "reports returned for every policy item" ($reports.Count -eq 5) "$($reports.Count) reports"

Write-Host "`n  observed statuses:" -ForegroundColor DarkGray
foreach ($r in $reports) { Write-Host ("    {0,-12} {1,-16} {2}" -f $r.id, $r.status, $r.message) -ForegroundColor DarkGray }

# --- 5. assertions against the real OS -------------------------------------
Section "firefox policy on disk"
$policyExists = Test-Path $FirefoxPolicy
$ffStatus = ReportStatus $reports 'firefox'
if ($policyExists) {
    $doc = Get-Content $FirefoxPolicy -Raw | ConvertFrom-Json
    $ublock = $doc.policies.ExtensionSettings.'uBlock0@raymondhill.net'
    $wildcard = $doc.policies.ExtensionSettings.'*'
    Check "policy file written to Program Files" $true $FirefoxPolicy
    Check "extension is force_installed" ($ublock.installation_mode -eq 'force_installed') "$($ublock.installation_mode)"
    Check "wildcard blocks other extensions" ($wildcard.installation_mode -eq 'blocked') "$($wildcard.installation_mode)"
    Check "telemetry disabled by default" ($doc.policies.DisableTelemetry -eq $true)
    Check "firefox report is honest (status matches disk)" ($ffStatus -in @('changed', 'compliant')) "$ffStatus"
} else {
    # App Management / permission problems are legitimate, but the agent must
    # then say so instead of claiming success.
    Check "missing policy is reported as action_required, not success" ($ffStatus -eq 'action_required') "$ffStatus"
}

Section "account state matches reality"
$admGroups = (Native 'net localgroup Administrators').Output
Check "standard user is not an administrator" ($admGroups -notmatch "\b$StdUser\b")
Check "admin user really is an administrator" ($admGroups -match "\b$AdmUser\b")
Check "user.account reports the standard user compliant" ((ReportStatus $reports 'std-user') -eq 'compliant') (ReportStatus $reports 'std-user')
Check "user.account reports the admin user drifted" ((ReportStatus $reports 'adm-user') -eq 'drifted') (ReportStatus $reports 'adm-user')

Section "default browser is never silently changed"
$choiceAfter = UserChoice
Check "browser.default never claims it changed anything" ((ReportStatus $reports 'browser') -ne 'changed') (ReportStatus $reports 'browser')
Check "UserChoice registry key untouched" ($choiceBefore -eq $choiceAfter) "before/after differ"
Check "browser.default gives the user an action" ((ReportStatus $reports 'browser') -in @('action_required', 'compliant')) (ReportStatus $reports 'browser')

# --- 6. idempotency --------------------------------------------------------
Section "idempotency"
$mtimeBefore = if (Test-Path $FirefoxPolicy) { (Get-Item $FirefoxPolicy).LastWriteTimeUtc } else { $null }
$genBefore = (Get-Content $CfgPath -Raw | ConvertFrom-Json).generation
& $Agent once -config $CfgPath -log-level error | Out-Null
Start-Sleep -Seconds 1
$genAfter = (Get-Content $CfgPath -Raw | ConvertFrom-Json).generation
$mtimeAfter = if (Test-Path $FirefoxPolicy) { (Get-Item $FirefoxPolicy).LastWriteTimeUtc } else { $null }
Check "generation unchanged without a policy change" ($genBefore -eq $genAfter) "$genBefore -> $genAfter"
Check "policy file not rewritten on a no-op cycle" ($mtimeBefore -eq $mtimeAfter) "mtime changed"

# --- 7. service installation ----------------------------------------------
Section "service"
& powershell -NoProfile -ExecutionPolicy Bypass -File $Installer -Binary $Agent | Out-Null
$svc = Get-Service -Name FleetAgent -ErrorAction SilentlyContinue
Check "service created" ($null -ne $svc)
if ($svc) {
    for ($i = 0; $i -lt 20 -and $svc.Status -ne 'Running'; $i++) {
        Start-Sleep -Milliseconds 500
        $svc.Refresh()
    }
    Check "service is running" ($svc.Status -eq 'Running') "$($svc.Status)"
    Check "service is automatic" ((Get-CimInstance Win32_Service -Filter "Name='FleetAgent'").StartMode -eq 'Auto')
}
& powershell -NoProfile -ExecutionPolicy Bypass -File $Installer -Uninstall | Out-Null
Start-Sleep -Seconds 2
Check "uninstall removes the service" ($null -eq (Get-Service -Name FleetAgent -ErrorAction SilentlyContinue))

# --- summary ---------------------------------------------------------------
$total = $script:results.Count
$passed = $total - $script:failed
Write-Host "`n===== $passed/$total checks passed =====" -ForegroundColor $(if ($script:failed -eq 0) { 'Green' } else { 'Red' })

$script:results | ConvertTo-Json -Depth 5 | Set-Content -Path 'C:\fleet-e2e-results.json' -Encoding utf8
exit $(if ($script:failed -eq 0) { 0 } else { 1 })
