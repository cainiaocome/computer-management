# Fleet — Design Document

A small, self-hosted computer management system for a handful of machines you own:
your own Windows/macOS/Linux boxes, and machines belonging to family members you
support (e.g. a parent's laptop).

Working name: **fleet** (repo: `computer-management`).

---

## 1. Problem and goals

### Goals

1. **Declarative management.** Describe the desired state of a machine in a policy
   bundle. Agents converge to it and report drift. No imperative "run this command
   as root" channel.
2. **Boring, repeatable onboarding.** One enrolment token per machine, one
   bootstrap checklist, then the agent keeps the machine in shape forever.
3. **Browser control.** Firefox as the default browser, a fixed set of extensions
   force-installed and non-removable by the user, sane default preferences.
4. **Low-ops remote support.** See all machines, their compliance state, and reach
   them over a mesh VPN when something is wrong.
5. **Zero recurring cost.** No Apple Developer Program, no Intune, no Jamf.
6. **Honest about limits.** Where an OS refuses to allow something (default browser
   on Windows 11, TCC grants without MDM), the system *detects and nudges* instead
   of silently failing or lying.

### Non-goals

- Being a commercial MDM. No ADE/zero-touch, no App Store app deployment, no
  per-app VPN, no compliance attestation.
- Managing devices you do not own or have explicit, informed consent for.
- Endpoint detection and response, DLP, or surveillance.
- A general remote-shell / RAT. Explicitly excluded — see §7.

### Success criteria

- Fresh macOS or Windows machine → enrolled and compliant in **≤ 10 minutes of
  human time**, including the GUI clicks that cannot be scripted.
- A parent's machine survives OS updates, browser updates, and user fiddling
  without a support call: the agent detects drift and either reverts it or posts a
  single actionable prompt.
- The server holds no credential that can *directly* run a command on a device.
  It can still write files as root, which is a code-execution primitive in
  practice; §7.1 states that boundary exactly.

---

## 2. The three gate types (the central design constraint)

Most home-grown management projects fail because they assume "root solves
everything". It does not. Every desired-state item falls into one of three
categories, and the implementation strategy differs completely.

| Gate | Example | Root/SYSTEM enough? | Strategy |
|---|---|---|---|
| **Consent gate** | macOS TCC (Full Disk Access, App Management, Screen Recording, Accessibility), Windows UAC, Gatekeeper "Open Anyway" | ❌ Subject to consent regardless of uid | Spend the consent budget **once, at provisioning**, by a human who is physically present |
| **Identity gate** | Windows `UCPD.sys` (default browser), Gatekeeper's "Apple-issued Developer ID + notarized" rule, SIP, Defender PPL | ❌ No dialog exists to consent to | Change **what the binary is** (signing, MDM, publisher) or avoid the gate entirely |
| **Architectural gate** | Windows session 0 isolation, per-user registry hives, per-user LaunchServices on macOS | ❌ Not a permission problem | Run code in the **right execution context** (two-tier agent) |

### 2.1 Consent gates: pre-grant once, then protect the grant

Consent gates are cheap because the operator is standing at the machine. But the
grant must survive:

- **Identity stability.** macOS TCC binds a grant to a *code identity* (designated
  requirement), not a path. Ad-hoc signing changes identity on every rebuild →
  the user is re-prompted on every update. Therefore: **sign the agent with a
  stable self-signed certificate**, install it to a fixed path, keep a fixed
  bundle identifier / binary path.
- **Non-admin daily user.** An admin user can re-toggle TCC, delete the daemon, or
  `sudo rm` anything. TCC changes require admin authorisation, so a *standard*
  daily account cannot revoke grants made by the operator's admin account. This is
  the single highest-leverage control in the whole design.
- **OS upgrades.** macOS 15+ periodically re-prompts for Screen Recording. Treat
  every consent grant as a **compliance item the agent reports on**, not a
  fire-and-forget setup step.

There is **no CLI to grant TCC**. `tccutil` only resets, and `TCC.db` is
SIP-protected even against root. The bootstrap step is therefore a **manual GUI
checklist**, and the agent must report `consent: missing` until it is complete.

### 2.2 Identity gates: no dialog exists

- **Windows `UCPD.sys`** (User Choice Protection Driver, Win11 22H2+) denies
  writes, deletes, renames and ACL changes to `UserChoice` keys for `http`,
  `https`, `.pdf`, `.htm(l)`, Office extensions — **to any non-Microsoft binary,
  including SYSTEM**. It detects renamed binaries via the PE `OriginalFilename`
  resource and has dynamic, Microsoft-updatable rules. Consequence: **never
  attempt to set the default browser programmatically on Windows.** Set it once by
  hand in Settings during provisioning; afterwards *detect* drift and *nudge*.
- **Gatekeeper.** The durable answers are (a) never let the file acquire the
  `com.apple.quarantine` xattr (install via `curl`/`ssh`, not a browser download),
  or (b) register a custom assessment requirement:
  `spctl --add --type execute --requirement 'certificate 1 H"<sha1 of CA>"'`.
  Trusting a custom root in the keychain *alone* is not enough, and
  `spctl --add` is deprecated and may disappear.
- **SIP / PPL / Secure Boot** are out of reach by design. Do not fight them.

### 2.3 Architectural gates: run in the right context

| Tier | Runs as | Can do |
|---|---|---|
| **Executor** | Windows service (LocalSystem), macOS `LaunchDaemon` (root) | packages, services, `/Library` and `HKLM` files, local users, firewall, browser policy files |
| **Session** | per-user process at login (`LaunchAgent` / user logon task) | default-browser nudge/set, per-user prefs, TCC-visible actions, UI prompts, tray indicator |

A root process cannot become the logged-in user's GUI session. On Windows,
LocalSystem lives in **session 0** and cannot show UI or touch a user's desktop;
`HKCU` work needs impersonation or a user-context task. On macOS you need
`launchctl asuser <uid>`. Fast User Switching means there can be several sessions.

**Rule:** every policy key has exactly one owning tier. If a key needs consent or a
session, it belongs to the session tier — and the root tier must not pretend
otherwise.

---

## 3. Architecture

```
        ┌────────────────────────────────────────────┐
        │                server (Go)                 │
        │  HTTP API · policy store (SQLite) · audit  │
        │  Ed25519 policy signing · enrolment tokens │
        └───────────────▲───────────────▲────────────┘
                        │ HTTPS, agent-initiated outbound only
        ┌───────────────┴───────┐   ┌───┴───────────────────┐
        │  executor tier        │   │  session tier         │
        │  (root / LocalSystem) │   │  (logged-in user)     │
        │  apply + verify       │   │  user-scope items     │
        └───────────────────────┘   └───────────────────────┘
                        │
              mesh VPN (Tailscale) for support access — optional, out of band
```

- **Agents dial out only.** No inbound ports, no NAT traversal, no listening
  socket on the device. Works from a parent's home network with zero router
  configuration.
- **Polling, not push** (~5 min with jitter, backoff on failure). Push requires
  APNs/FCM or a persistent connection; polling is one HTTP GET and is good enough
  for a fleet of tens.
- **The server is authoritative but not omnipotent.** It ships *policy documents*,
  never commands. See §7.

### 3.1 Why Go

Single static binary per platform (`CGO_ENABLED=0`), first-class cross-compilation,
stdlib `net/http` and `crypto/*` cover everything, trivial to install as a Windows
service or a `launchd` job, and one language for agent + server keeps the wire
types shared in one package.

---

## 4. Policy model

### 4.1 Enforcement classes

Every policy item declares how it is enforced. This is what stops the agent from
lying about success.

| Class | Meaning | On drift |
|---|---|---|
| `enforce` | Root tier can fully control it | Revert silently, report `compliant`/`drifted` |
| `converge_user` | Session tier applies it as the user | Re-apply at login and every poll |
| `nudge` | Cannot be applied programmatically (identity gate) | Detect, then surface a one-click action; report `action_required` |
| `bootstrap_once` | Requires a human GUI step (consent gate) | Report `missing` + instructions; never claim success |
| `report_only` | Observability without enforcement | Record only; never change the machine |

### 4.2 Bundle format

A policy bundle is canonical JSON, Ed25519-signed by the server:

```json
{
  "schema": 1,
  "device_id": "dev_01H...",
  "generation": 7,
  "issued_at": "2026-11-24T12:00:00Z",
  "policies": [
    { "id": "firefox/base",  "type": "firefox.policy",    "class": "enforce",        "config": { } },
    { "id": "apps/base",     "type": "packages",          "class": "enforce",        "config": { } },
    { "id": "browser",       "type": "browser.default",   "class": "nudge",          "config": { "browser": "firefox" } },
    { "id": "consent",       "type": "macos.consent",     "class": "bootstrap_once", "config": { } }
  ],
  "signature": "base64(ed25519('fleet-policy-bundle-v1\\n' + canonical_json_without_signature))"
}
```

- `generation` is monotonically increasing per device; an agent refuses a bundle
  with a generation lower than the one it already holds (anti-rollback).
- The agent **pins the server's signing key** on first enrolment (TOFU) and
  refuses any bundle that does not verify. There is no "accept unsigned policy"
  mode, not even a debug flag.
- Policy is per **group** (e.g. `parents`, `self`, `work`) with per-device
  overrides, resolved server-side so the agent receives a flat list.

### 4.3 Module set (MVP)

| Type | Class | Platform | Description |
|---|---|---|---|
| `firefox.policy` | enforce | all | Write `policies.json`; force-install extensions, lock prefs |
| `packages` | enforce | all | `winget` / `brew` / `apt` package presence |
| `file` | enforce | all | File content, mode, owner |
| `user.account` | enforce | all | Ensure a non-admin daily account exists |
| `browser.default` | nudge | all | Detect default browser; open the right settings pane |
| `macos.consent` | bootstrap_once | macOS | Report TCC grant state + instructions |
| `inventory` | report_only | all | OS version, disk, installed browsers, last seen |

Everything is **idempotent and verifiable**: each module has `Plan`, `Apply`, and
`Verify`, and the agent reports the *observed* state after applying, not the
intended one.

### 4.4 Firefox specifics

Firefox policy is the one place where "cannot be uninstalled" is actually
achievable, and it takes two layers:

1. **Policy layer** — `ExtensionSettings` with
   `"installation_mode": "force_installed"`. The extension appears as *Managed*,
   and the disable/remove buttons are greyed out. Also `blocked_install_message`,
   `Preferences`, `DisableTelemetry`, etc.
2. **Account layer** — the daily user is not an admin, so they cannot edit
   `/Applications/Firefox.app/Contents/Resources/distribution/policies.json` or
   `HKLM\SOFTWARE\Policies\Mozilla\Firefox`.

Policy file locations:

| OS | Path |
|---|---|
| Windows | `C:\Program Files\Mozilla Firefox\distribution\policies.json` (HKLM registry alternative) |
| macOS | `/Applications/Firefox.app/Contents/Resources/distribution/policies.json` — writing this needs **App Management** consent on macOS 13+ |
| Linux | `/etc/firefox/policies/policies.json` |

Notes / risks:

- On macOS the bundle path is fragile across Firefox updates; verify per version
  and pin ESR for managed machines.
- Chrome/Edge are materially easier on macOS (root-owned managed preference
  plists, no app-bundle edits, no App Management prompt). If forced extensions on
  macOS matter more than Firefox specifically, prefer Chrome. This is a real
  trade-off, recorded here deliberately.

---

## 5. Protocol

All endpoints under `/api/v1`. JSON. TLS is the deployment's responsibility
(reverse proxy or mesh VPN); the policy signature is what the agent trusts.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/enroll` | enrolment token | Exchange a single-use token for `device_id` + `device_secret` + signing pubkey |
| `POST` | `/checkin` | device secret | Send observed state, receive signed bundle |
| `POST` | `/heartbeat` | device secret | Keepalive, cheap |
| `GET` | `/admin/devices` | admin token | List devices, compliance, last seen |
| `POST` | `/admin/devices/{id}/policy` | admin token | Assign group / overrides |
| `POST` | `/admin/enrollment-tokens` | admin token | Mint a single-use token (TTL, use count) |
| `POST` | `/admin/groups/{name}/policy` | admin token | Set group policy |
| `GET` | `/healthz` | none | Liveness |

- Device secret is a 32-byte random value, stored server-side as a SHA-256 hash.
- Enrolment tokens are single-use, TTL-bounded, stored hashed, and bound to an
  optional hostname prefix.
- All admin actions are written to an append-only audit log (`actor`, `action`,
  `target`, `payload`, `timestamp`).

---

## 6. Data model

```
devices(id, hostname, os, os_version, arch, group_name, generation,
        enrolled_at, last_seen_at, device_secret_hash, state_json)
groups(name, policy_json, updated_at)
enrollment_tokens(token_hash, label, expires_at, used_at, max_uses, uses)
audit_log(id, at, actor, action, target, detail)
server_keys(kid, public_key, private_key_enc)   -- or key file on disk
```

SQLite (pure-Go driver, no cgo) is enough for tens of devices. The store is behind
an interface so Postgres is a later swap, not a rewrite.

---

## 7. Threat model

The agent is a root-level daemon on every managed machine. That makes the
**server the most valuable target in the system** — a compromised server that can
run arbitrary code as root is ransomware-as-a-service with your devices as the
fleet.

Mitigations, in priority order:

1. **No direct command execution path.** The API has no "run script" endpoint,
   and the wire format only expresses *declarative policy over a fixed module
   set*: there is no `exec` primitive. The remaining primitive is `file`, which
   writes as root; §7.1 is explicit about what that costs.
2. **Signed bundles + pinning.** The agent verifies an Ed25519 signature over the
   canonical bundle and ignores anything unsigned or unverifiable. A MITM on the
   transport cannot inject policy.
3. **Anti-rollback.** `generation` must be strictly increasing.
4. **Narrow blast radius.** Modules are small, reviewed, and individually
   disableable; `report_only` is the default class for anything new.
5. **No secrets on the device** beyond the device secret. That secret can fetch
   this device's policy *and* post reports for it, so it must be readable only by
   SYSTEM/Administrators: a local user who can read it can fake compliance.
6. **Least privilege in the daemon.** The executor drops to a dedicated user for
   package-manager work where possible; it does not read user data.
7. **Auditability.** Every applied change is reported back with before/after and
   is visible in the dashboard.
8. **Consent and disclosure.** Managed machines get a visible indicator (menu bar
   / tray item, or a login banner) and the operator documents what is collected.
   For family machines this is a conversation, not a checkbox.

Explicitly out of scope: defending against an attacker who already has root on the
device (they can uninstall the agent), or against the device owner booting
Recovery/WinRE. "Unremovable" is a *policy* property (non-admin user) and a
*durability* property (re-assert loop), never an absolute one.

---

## 8. Provisioning checklists

These are the human steps that cannot be automated. The agent reports each as a
compliance item so the operator can see what is still outstanding.

### 8.1 macOS (fresh machine)

1. Create the **operator admin account first** (Apple Silicon/FileVault: it must
   exist before the daily account, or it will not get a Secure Token).
2. Install the agent via `curl … | sudo sh` (no quarantine xattr → no Gatekeeper
   interaction), or over SSH/Tailscale.
3. Verify the agent's signature; if distributing `.app`/`.pkg` bundles later, use
   the custom-CA + `spctl` requirement recipe.
4. Grant, in System Settings → Privacy & Security: **App Management** (needed for
   the Firefox policies file), and Full Disk Access / Screen Recording **only if
   the relevant feature is enabled**.
5. Create the daily account as a **standard** user; demote/remove the bootstrap
   admin from the `admin` group unless it is the operator account.
6. Enrol: `sudo fleet-agent enroll --server https://… --token …`.
7. Set Firefox as default browser once (by hand or `defaultbrowser` as that user).
8. Confirm the dashboard shows `compliant`.

### 8.2 Windows (fresh machine)

1. Install the agent as a service (`fleet-agent install`). UAC → yes.
2. Defender exclusions via supported APIs if needed (`Set-MpPreference`); Tamper
   Protection blocks the registry route.
3. **Set the default browser by hand in Settings → Default apps.** Never
   programmatically — `UCPD.sys` will deny it and may report the attempt.
4. Create the daily account as a **standard** user; keep the operator account.
5. Enrol and confirm `compliant`.

### 8.3 Both

- Add the machine to the mesh VPN (Tailscale) for support access — out of band, not
  managed by the agent.
- Record the enrolment token label so the audit log is legible later.

---

## 9. Testing strategy

- **Unit tests** — module `Plan`/`Apply`/`Verify` logic is pure-function heavy and
  runs on all three OSes in CI. Firefox policy rendering and bundle signing are
  pure functions with golden files.
- **Handler tests** — `httptest` against the real server with a temp SQLite file;
  covers enrolment, replay, expired token, bad signature, generation rollback.
- **Agent integration** — `apply --once` against a local server in a temp
  filesystem root (`--root` override) on all three runners, verifying the files
  the agent writes and the state it reports.
- **Real-OS validation** runs on GitHub-hosted Windows/macOS runners (which are
  real operating systems) via `test/e2e/guest/verify.*`, and locally in a real VM
  with `test/e2e/vm/*/run.sh`. See [`../test/e2e/README.md`](../test/e2e/README.md).
  Platform internals are documented in [`platforms.md`](platforms.md).

Non-goals for CI: booting macOS VMs inside GitHub Actions (licensing/feasibility),
and TCC/UCPD behaviour, which is only observable on a real machine and is covered
by the checklists in §8.

---

## 10. Rollout plan

| Phase | Deliverable | Exit criteria |
|---|---|---|
| 0 | This document, bootstrap checklist, mesh VPN | A parent's machine configured by hand once |
| 1 | Server + agent + `firefox.policy`, `packages`, `inventory`; enrolment; compliance view | Fresh machine → compliant, no manual config beyond §8 |
| 2 | `service` module, `browser.default` nudge, `macos.consent` reporting, drift detection, audit UI | Drift is detected within one poll interval on both OSes |
| 3 | Auto-update (signed, Sparkle-style manifests), rollback, group inheritance | Agent updates itself without losing TCC grants |
| 4 | Only if genuinely needed: real macOS MDM (MicroMDM/Fleet) for PPPC + FileVault escrow | — |

Phase 4 is deliberately last. Apple's $99/yr buys silent PPPC grants, FileVault
escrow and non-removable profiles; if the fleet is a handful of family machines
and the daily users are non-admin, the provisioning checklist covers the same
ground for free.

---

## 11. Open questions

- Firefox ESR vs release on managed machines (bundle-path fragility on macOS).
- Whether to ship Chrome as the macOS default and Firefox elsewhere, given the
  App Management friction on macOS.
- Self-update trust chain without a paid signing identity (Ed25519 manifest +
  `curl`-style install avoids Gatekeeper entirely; Authenticode/SmartScreen on
  Windows remains a papercut).
- Where the operator's admin credentials live, and how recovery works if the
  operator is unavailable.
