# Provisioning a machine

The steps in this document are the ones that **cannot be automated**: consent
gates (TCC, UAC) and identity gates (`UCPD.sys`, Gatekeeper) either need a human
click or refuse third-party binaries outright. See `docs/design.md` §2.

Budget ~10 minutes of hands-on time per machine.

---

## 0. Before touching the machine

- **Get consent.** If the machine belongs to someone else, tell them what is
  managed, what is collected (inventory: OS version, hostname, installed
  browsers, compliance status — not personal files) and how to reach you.
- **Have the operator admin account ready.** You keep the admin password; the
  daily user will be a standard account. This is what makes browser policy, TCC
  grants and the agent itself durable.
- **Have the server reachable** (TLS or a mesh VPN) and an enrolment token minted:

  ```bash
  curl -s -X POST https://fleet.example.com/api/v1/admin/enrollment-tokens \
    -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
    -d '{"label":"mom-macbook","ttl_seconds":3600,"max_uses":1}'
  ```

- **Join the machine to the mesh VPN** (e.g. Tailscale) if you want remote
  support. This is out of band and not managed by the agent.

---

## 1. macOS

### 1.1 Create your admin account *first*

On Apple Silicon (and with FileVault enabled), account creation needs a Secure
Token, and the first account is the one that gets it. Sequence:

1. During Setup Assistant, create the **operator admin** account (you).
2. After setup, create the **daily user** and make it a standard account:
   System Settings → Users & Groups → Add User → *Standard*.
3. Leave the operator account in the admin group; the daily user must not be.

If the daily user is an admin, almost every guarantee in this project disappears:
they can re-toggle TCC, delete the daemon, and remove browser policy.

### 1.2 Install the agent

Install with `curl`/`ssh`/Tailscale, **not** a browser download — files fetched by
`curl` never receive the `com.apple.quarantine` attribute, so Gatekeeper does not
get a chance to block them.

```bash
sudo scripts/install-unix.sh -b ./fleet-agent -s https://fleet.example.com -t <TOKEN>
```

This copies the binary to `/usr/local/bin/fleet-agent` and installs
`/Library/LaunchDaemons/com.fleet.agent.plist`.

### 1.3 Optional: stable code signing (recommended, free)

macOS binds TCC grants to the **code identity**. An ad-hoc signed binary changes
identity on every rebuild, which re-prompts for every grant after each update.
Use one stable self-signed identity:

```bash
# 1. create a CA + code-signing leaf (Keychain Access > Certificate Assistant,
#    or cfssl/openssl). Leaf needs EKU codeSigning (critical).
codesign --force --sign "Fleet Code Signing" --identifier com.fleet.agent ./fleet-agent
codesign -dv --verbose=4 ./fleet-agent     # note the TeamIdentifier / identifier

# 2. trust the CA for code signing only (NOT for TLS: never trust it for ssl)
sudo security add-trusted-cert -d -r trustRoot -p codeSign \
     -k /Library/Keychains/System.keychain fleet-ca.pem

# 3. allow code signed by that CA, no notarization needed
sudo spctl --add --type execute --label 'Fleet CA' \
     --requirement 'certificate 1 H"<SHA1 of the CA certificate>"'
spctl --assess --type execute -vv /usr/local/bin/fleet-agent
```

`spctl --add` is deprecated; if a future macOS drops it, the fallback is to keep
distributing via `curl`/`ssh` so quarantine is never set.

### 1.4 Grant the consent that your policy actually needs

System Settings → Privacy & Security. Grant the minimum:

| Grant | When you need it | Why |
|---|---|---|
| **App Management** | Firefox policy on macOS | writing `Firefox.app/Contents/Resources/distribution/policies.json` modifies another app's bundle |
| Full Disk Access | only if a module reads user data | otherwise pointless and a privacy liability |
| Screen Recording | only if you embed remote support | macOS 15+ re-prompts periodically anyway |

Drag `/usr/local/bin/fleet-agent` into the App Management list (or add it with
`+`). There is no CLI for this: `tccutil` only resets, and `TCC.db` is
SIP-protected even against root.

### 1.5 Enrol and verify

```bash
sudo fleet-agent enroll -server https://fleet.example.com -token <TOKEN>
sudo fleet-agent status
sudo fleet-agent once -log-level debug      # apply immediately instead of waiting
```

Then, on the server:

```bash
curl -s https://fleet.example.com/api/v1/admin/devices \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" | jq '.devices[] | {hostname, generation, compliance}'
```

### 1.6 Set the default browser once

By hand, or as the daily user:

```bash
defaultbrowser firefox     # https://github.com/mkdom/defaultbrowser
```

Expect `browser.default` to report `action_required` until this is done. It will
never silently succeed, because macOS keeps the default handler in per-user
LaunchServices state.

---

## 2. Windows

### 2.1 Install as a service

From an elevated PowerShell prompt:

```powershell
.\scripts\install-windows.ps1 -Binary .\fleet-agent.exe -Server https://fleet.example.com -Token <TOKEN>
```

Runs as LocalSystem (`FleetAgent` service), restarts on failure.

Unsigned binaries will trip SmartScreen/Defender reputation on some machines.
Sign the binary with Authenticode (Azure Trusted Signing is cheap) if that
becomes a support burden.

### 2.2 Set the default browser **by hand**

Settings → Apps → Default apps → Firefox → *Set default*.

Do not script this. Windows 11's `UCPD.sys` is a kernel driver that denies write,
delete, rename and ACL changes to the `UserChoice` keys to any non-Microsoft
binary, **including SYSTEM**, and it reports attempts. `browser.default` therefore
only detects and nudges.

### 2.3 Create the daily user as a standard account

```powershell
net user mom /add
net localgroup Users mom /add
# do NOT add to Administrators
```

The `user.account` module reports `drifted` if the account is an admin, and only
demotes when the policy item sets `"demote": true`.

### 2.4 Optional: Defender exclusion

Only if Defender is interfering with package installs. Use the supported API, not
the registry (Tamper Protection blocks registry edits and may revert them):

```powershell
Add-MpPreference -ExclusionPath "C:\Program Files\Fleet"
```

### 2.5 Verify

```powershell
Get-Service FleetAgent
& "C:\Program Files\Fleet\fleet-agent.exe" status
& "C:\Program Files\Fleet\fleet-agent.exe" once -log-level debug
```

---

## 3. Reading the compliance report

| Status | Meaning | Action |
|---|---|---|
| `compliant` | observed state matches policy | none |
| `changed` | agent just applied it (next cycle will read `compliant`) | none |
| `drifted` | differs and was **not** fixed (dry-run, or demote not enabled) | inspect, enable the fix |
| `action_required` | identity/consent gate: a human must click | follow the `detail.open` deep link |
| `unsupported` | wrong platform or missing tool (e.g. no `brew`) | install the tool or drop the item |
| `error` | the module failed | check `message`, then agent logs |

A device that keeps flapping between `compliant` and `drifted` is being changed by
someone else — usually the user, or a browser update that replaced the policy
file. That is exactly what the re-assert loop is for.

---

## 4. Uninstall / recover

macOS:

```bash
sudo launchctl bootout system /Library/LaunchDaemons/com.fleet.agent.plist
sudo rm /Library/LaunchDaemons/com.fleet.agent.plist /usr/local/bin/fleet-agent
# Firefox policy: remove the distribution/policies.json file you installed
```

Windows (elevated):

```powershell
.\scripts\install-windows.ps1 -Uninstall
```

If you lose access to the server, devices keep enforcing their last signed
generation. That is intentional: the policy is local and signed, not a live
connection to the server.

---

## 5. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `rejecting policy bundle: signature is not valid` | server key changed (new `-key` file) | re-enrol the device, or restore the old key |
| `unknown device` on check-in | device secret rotated or DB restored from an older backup | re-enrol |
| Firefox extension still removable | daily user is a local admin | demote the account; policy alone is not enough |
| Firefox policy disappears after an update | bundle path replaced by the updater | re-assert on next poll; consider pinning Firefox ESR |
| `cannot write Firefox policy: App Management consent is required` | macOS TCC | grant App Management to the agent binary, and keep its code identity stable |
| `action_required` for browser.default forever | nobody clicked "set default" | do it once, by hand |
| Agent enrolled but no bundle | device not in a group, or group has no policy | `PUT /api/v1/admin/devices/{id}` with `{"group":"..."}` |
