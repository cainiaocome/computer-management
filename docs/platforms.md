# Platform internals

Why the agent is written the way it is, per operating system. Read this before
adding a module: most of the hard limits here are not bugs in this project, they
are how the platform works.

Related: [`design.md`](design.md) (the gate taxonomy), [`provisioning.md`](provisioning.md)
(the human-only steps).

- [1. Windows](#1-windows)
- [2. macOS](#2-macos)
- [3. Linux](#3-linux)
- [4. Cross-platform comparison](#4-cross-platform-comparison)

---

## 1. Windows

### 1.1 Where configuration lives

| Mechanism | Location | Enforced by | Root/SYSTEM enough? |
|---|---|---|---|
| Registry (plain settings) | `HKLM\SOFTWARE\...`, `HKCU\SOFTWARE\...` | the app itself | yes for HKLM |
| Administrative Templates / policies | `HKLM\SOFTWARE\Policies\<Vendor>\<App>` | the app, at startup and periodically | yes |
| Group Policy | pulled from a DC, written into the above | `gpsvc`, refreshed every 90 min ±30 | only on a domain |
| Local GPO | `%SystemRoot%\System32\GroupPolicy` | `gpsvc` | yes, via `LGPO.exe` |
| MDM (OMA-DM) | enrolled to an MDM server | `dmclient` → `MDM_*` WMI providers | needs MDM enrolment |
| Task Scheduler | `\Microsoft\Windows\...` | `schedule` service | yes |
| Services | `HKLM\SYSTEM\CurrentControlSet\Services` | SCM | yes |

Notes:

- `HKLM\SOFTWARE\Policies` is the *documented* management surface; applications must
  not let users override it. `HKLM\SOFTWARE\<Vendor>` without `\Policies` is ordinary
  configuration — writing it works but the app is free to rewrite it.
- **Group Policy is not available on Windows Home.** Home also lacks AppLocker,
  `DefaultAssociationsConfiguration`, and a usable `gpedit.msc`. Registry and
  services still work, which is why the agent prefers them.
- MDM for Windows needs a real OMA-DM server (SOAP over HTTPS, certificate auth).
  Implementing that is a project in itself; this project deliberately does not.

### 1.2 Privilege model

- **LocalSystem (SYSTEM)** has `SeTcbPrivilege`, full local access, and no user
  profile. A service running as SYSTEM can write `HKLM` and `%ProgramFiles%`.
- **A process is not "root" in the Unix sense.** Integrity levels, ACLs and kernel
  filters still apply. Windows has three real ceilings above SYSTEM:
  1. **Session 0 isolation** — services cannot interact with a user's desktop, and
     cannot show UI. Per-user work needs a token (`LogonUser` +
     `CreateProcessAsUser`, or a scheduled task in the user's context).
  2. **Protected Process Light (PPL)** — Defender and other AV run as protected
     processes; even SYSTEM cannot open or stop them.
  3. **Kernel-enforced policy** — e.g. `UCPD.sys` (below) decides by *binary
     identity*, not by privilege.
- **UAC token filtering**: an administrator who logs on interactively gets a
  *filtered* token; elevation requires consent. Over the network (SMB/SSH) local
  accounts get a filtered token too unless
  `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\LocalAccountTokenFilterPolicy=1`
  — which is why the VM test harness sets it. Never set it on a real machine.
- **Tamper Protection** stops even SYSTEM from disabling Defender, changing
  exclusions via the registry, or removing protection. Use `Set-MpPreference`
  (the supported API) at provisioning time.

### 1.3 Default browser: `UCPD.sys`

The association for `http`/`https`/`.pdf`/`.htm(l)`/Office extensions lives in:

```
HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice
    ProgId = FirefoxURL-...
    Hash   = <base64 hash over (SID, ProgId, timestamp)>
```

Two independent defences:

1. **Hash validation.** The `Hash` value is derived from the user's SID, the
   ProgId and a timestamp. Writing `ProgId` without a matching `Hash` makes Windows
   reset the association. The algorithm is undocumented and changed between builds;
   third-party tools that recompute it (`SetUserFTA`) break regularly.
2. **`UCPD.sys`** (User Choice Protection Driver, Windows 11 22H2+). A kernel
   driver that denies write/delete/rename/ACL changes to those keys to any
   non-Microsoft binary — **regardless of privilege, including SYSTEM**. It
   identifies callers by image identity and detects renamed binaries via the PE
   `OriginalFilename` resource. Microsoft can update its rules at runtime, and it
   logs attempts.

There is **no consent dialog** to click for a third-party binary, which is why this
is an *identity gate*: the only supported programmatic path is the MDM
`DefaultAssociationsConfiguration` CSP (a provisioning XML applied by `dmclient`),
and that needs Pro/Enterprise + MDM.

Practical consequence for this project: `browser.default` detects and nudges. It
has no write path at all, and a test asserts that.

### 1.4 Firefox policy

`policies.json` in the installation directory:

```
C:\Program Files\Mozilla Firefox\distribution\policies.json
C:\Program Files (x86)\Mozilla Firefox\distribution\policies.json   (32-bit)
```

An HKLM registry equivalent exists under `HKLM\SOFTWARE\Policies\Mozilla\Firefox`,
which is the better choice when the install directory is unpredictable. Firefox
reads policies at startup; a change needs a browser restart.

Relevant policy keys used by this project:

| Key | Effect |
|---|---|
| `ExtensionSettings.<id>.installation_mode = force_installed` | extension is installed, cannot be removed or disabled, shown as *Managed* |
| `ExtensionSettings."*".installation_mode = blocked` | users cannot install anything else |
| `ExtensionSettings."*".blocked_install_message` | text shown when they try |
| `ExtensionUpdate` | keeps managed extensions updated |
| `Preferences.<pref>` | arbitrary pref, user cannot override |
| `DefaultBrowserSettingEnabled` | Firefox checks whether it is default and offers to fix it |
| `DisableTelemetry`, `DisablePocket`, `DisableFirefoxAccounts` | privacy defaults |

"Force-installed" is enforced by the browser, so it holds only while the policy
file is readable. An administrator can delete the file; a standard user cannot.
That is the whole design: **policy + non-admin account**, not policy alone.

### 1.5 Packages

`winget` (App Installer) is user-scope by default. For machine-wide installs the
agent runs it as SYSTEM with `--scope machine` where supported; many manifests
ignore scope. `winget list --id <id> --exact` is the verification, and its exit
code is unreliable — parse the output.

Alternatives: `choco`/`scoop` (third-party), MSI via `msiexec /qn`, or
`Add-AppxPackage` for Store apps (needs a user context).

### 1.6 Services

The agent installs itself with `sc create` (or `New-Service`) as `LocalSystem`,
`start= auto`, with a failure/recovery policy. `sc failure <name> reset= 86400
actions= restart/30000/...`. Note: a service in `SESSION 0` cannot show UI and
must not block on user input.

### 1.7 What an agent *cannot* do on Windows

- Set the default browser programmatically (UCPD).
- Disable Defender or add exclusions through the registry (Tamper Protection).
- Show UI from a service (session 0).
- Change per-user settings without an impersonation token.
- Block Recovery/WinRE, or stop a local admin from deleting the service.
- Enforce anything on Windows Home that requires GPO/AppLocker.

---

## 2. macOS

### 2.1 Where configuration lives

| Mechanism | Location | Notes |
|---|---|---|
| Managed preferences (MCX) | `/Library/Managed Preferences/`, `/Library/Preferences/` | root-owned plists; Chrome/Edge read these natively |
| Plain preferences | `~/Library/Preferences/<bundleid>.plist` | per user, user-writable |
| Configuration profiles | installed into the system | only *non-removable* when pushed by MDM |
| launchd jobs | `/Library/LaunchDaemons`, `/Library/LaunchAgents`, `~/Library/LaunchAgents` | daemon vs agent |
| App bundles | `/Applications/<App>.app/Contents/...` | writing here needs App Management consent (macOS 13+) |
| MDM | APNs-driven command channel | needs an Apple-issued MDM push cert |

**Chrome and Edge are materially easier than Firefox on macOS**: they read
root-owned `/Library/Managed Preferences/com.google.Chrome.plist` directly, so a
root daemon can manage them with no bundle edits and no TCC prompt. Firefox has no
equivalent macOS managed-preferences domain that this project relies on; its
macOS policy is the `policies.json` file *inside* the app bundle.

### 2.2 The privilege model, and why root is not enough

macOS runs several independent authorities **above** uid 0:

1. **SIP** (System Integrity Protection, `csrutil`). Protects `/System`,
   `/usr` (most), Apple-signed binaries, and — critically — `TCC.db`. Root cannot
   write those paths. Disabling SIP requires Recovery mode; a management agent
   must never require that.
2. **SSV** (Signed System Volume). The system volume is a signed APFS snapshot;
   even root modifications are rejected at mount level.
3. **TCC** (Transparency, Consent, Control). Per-*code-identity* grants for
   privacy-sensitive capabilities. **TCC applies to root.** A root daemon without
   Full Disk Access gets `EPERM` reading `~/Library/Mail`. There is no
   `tccutil` subcommand to *grant*; `TCC.db` is SIP-protected. Grants come from
   (a) the user clicking in System Settings, or (b) an MDM PPPC profile.
4. **App Management** (macOS 13+): modifying another application's bundle is a
   TCC service. This is what bites when writing
   `/Applications/Firefox.app/.../policies.json` — the write fails with `EPERM`
   until the agent is granted App Management.

TCC services relevant here: `Full Disk Access`, `App Management`, `Accessibility`,
`Screen Recording`, `ListenEvent`, plus per-device camera/mic. Each is keyed to the
code signing identity, so **re-signing the binary resets every grant** — the reason
`docs/provisioning.md` insists on a stable self-signed identity.

### 2.3 Code signing, Gatekeeper, quarantine

- The kernel requires a valid signature to execute ARM64 binaries; `codesign -s -`
  (ad-hoc) is enough to run, but nothing more.
- Gatekeeper assesses *quarantined* files (those with the `com.apple.quarantine`
  xattr, set by browsers). Files fetched with `curl`/`ssh` have no such xattr, so
  an agent installed that way is never assessed.
- The default policy is "Apple-issued Developer ID **and** notarized". To bypass it
  legitimately you either (a) never set quarantine, or (b) register a custom
  assessment rule:

  ```bash
  sudo security add-trusted-cert -d -r trustRoot -p codeSign \
       -k /Library/Keychains/System.keychain fleet-ca.pem
  sudo spctl --add --type execute --label 'Fleet CA' \
       --requirement 'certificate 1 H"<SHA1 of CA cert>"'
  ```

  `-p codeSign` scopes the trust so a leaked CA key cannot MITM TLS. `spctl --add`
  is deprecated; treat (a) as the durable path.
- Notarization itself requires a Developer ID ($99/yr). Nothing in this project
  needs it.

### 2.4 launchd: daemon vs agent

| | LaunchDaemon | LaunchAgent |
|---|---|---|
| Path | `/Library/LaunchDaemons` | `/Library/LaunchAgents`, `~/Library/LaunchAgents` |
| Runs as | root (or `UserName`) | the logged-in user |
| Session | system, no GUI | user's GUI session |
| Can write `/Library`, `/Applications` | yes (subject to TCC) | no |
| Can touch LaunchServices defaults | no (wrong session) | yes |
| Loaded with | `launchctl bootstrap system <plist>` | `launchctl bootstrap gui/<uid> <plist>` |

Root cannot "become" the user's GUI session. Acting as a user requires
`launchctl asuser <uid> <cmd>` and even then the item is per-session. Fast User
Switching means several sessions can exist at once. That is the *architectural
gate* — no amount of privilege fixes it, which is why this project plans two tiers
(`docs/design.md` §2.3).

### 2.5 Default browser

The default handler is per-user `LaunchServices` state, stored in
`~/Library/Preferences/com.apple.LaunchServices/com.apple.launchservices.secure.plist`
as `LSHandlers` entries (`lsHandlerURLScheme = https` → `LSHandlerRoleAll =
org.mozilla.firefox`). It is *not* a managed preference, so a profile cannot set
it, and a root daemon cannot set it for another user. Tools like `defaultbrowser`
work by calling `LSSetDefaultHandlerForURLScheme` **in that user's session**.

Consequence: set it once by hand (or per-user), then detect and nudge.

### 2.6 MDM: what it is and what it costs

MDM on macOS is: the device checks in with your server over HTTPS, and your server
sends commands that the device *pulls* after receiving an APNs push. Requirements:

1. An **MDM push certificate**. You generate a push CSR, get it signed by an
   "MDM vendor" identity, upload it to `identity.apple.com`, and Apple returns the
   certificate whose topic (`com.apple.mgmt.External.<uuid>`) the device trusts.
   Today a standard **$99/yr Apple Developer account** can request the MDM CSR
   (the old $500 Enterprise-only requirement is gone). No local PKI can substitute:
   Apple's APNs gateway validates the sender.
2. An enrolment profile, delivered manually or automatically (ADE, which needs
   Apple Business Manager and a legal entity with a D-U-N-S number).

What MDM buys that root cannot:

| Capability | Without MDM | With MDM |
|---|---|---|
| PPPC: pre-approve TCC (FDA, Screen Recording, Accessibility) | user must click | silent, permanent |
| Profiles that the user cannot remove | removable by any admin | non-removable |
| FileVault key escrow | — | yes |
| OS update deferral/enforcement | — | yes |
| Remote lock/wipe, Activation Lock bypass | — | yes |
| Zero-touch (ADE) | — | yes |

For a few family machines with non-admin daily users, the provisioning checklist
covers most of it for free. Buy the $99 only when you need silent PPPC, FileVault
escrow, or a fleet big enough that manual setup hurts.

### 2.7 What an agent *cannot* do on macOS

- Grant itself TCC permissions, or edit `TCC.db` (SIP).
- Force the default browser (per-user LaunchServices).
- Modify `/System`, load unsigned kexts, or bypass Gatekeeper for quarantined apps.
- Write inside another app's bundle without App Management consent.
- Act in a user's GUI session from a root daemon.
- Survive a Recovery-mode deletion of the launchd job (nothing can).

---

## 3. Linux

Kept deliberately small: it is the platform the server runs on and a convenient
third test target.

| Concern | Mechanism |
|---|---|
| Service | systemd unit in `/etc/systemd/system`, `Restart=always` |
| Firefox policy | `/etc/firefox/policies/policies.json` (root-owned; no TCC equivalent) |
| Packages | `apt-get install -y`, verified with `dpkg-query -W -f='${Status}'` |
| Default browser | `xdg-settings get default-web-browser` (per-user, `~/.config/mimeapps.list`) |
| Privilege | root is genuinely enough, except for per-user session state |

Linux is the best place to see the *real* behaviour of the agent loop because
nothing is in the way: no TCC, no UCPD, no code signing requirement.

---

## 4. Cross-platform comparison

| Concern | Windows | macOS | Linux |
|---|---|---|---|
| Effective root | SYSTEM (session 0) | uid 0 (session-less daemon) | uid 0 |
| Consent layer above root | UAC + PPL + Tamper Protection | TCC + SIP + SSV | none |
| Identity gate | `UCPD.sys` (default browser) | Gatekeeper (code) | none |
| Per-user state needs a session | yes (token, session 0 isolation) | yes (`launchctl asuser`) | yes (user uid) |
| Force-install browser extensions | ✅ `policies.json` | ✅ `policies.json` in bundle | ✅ `policies.json` |
| Extension truly unremovable | policy + non-admin user | policy + non-admin user | policy + non-admin user |
| Force default browser | ❌ UCPD | ❌ per-user LaunchServices | ⚠️ `xdg-settings` (user) |
| Service manager | SCM (`sc.exe`) | launchd | systemd |
| Silent TCC-equivalent grant | n/a | needs MDM PPPC | n/a |
| Unattended OS install for tests | ✅ dockur/windows | ❌ interactive installer | ✅ containers |
