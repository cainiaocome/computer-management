# fleet

Declarative management for a handful of machines you own — your own Windows and
macOS boxes, and the laptop you look after for a family member.

You describe the desired state (browser policy, packages, which account is a
standard user), the agent converges to it, and the server shows you what drifted.

中文文档：[`README.zh-CN.md`](README.zh-CN.md)。

- Design and hard limits: [`docs/design.md`](docs/design.md)
- Platform internals (registry, TCC, UCPD, launchd, MDM): [`docs/platforms.md`](docs/platforms.md)
- Machine setup checklists: [`docs/provisioning.md`](docs/provisioning.md)
- End-to-end tests (three tiers, real VMs): [`test/e2e/README.md`](test/e2e/README.md)

## What it does

| Capability | Windows | macOS | How |
|---|---|---|---|
| Firefox policy: force-install extensions, lock prefs | ✅ | ✅ | `policies.json` |
| Force-installed extensions the user cannot remove | ✅ | ✅ | browser policy **+ non-admin daily user** |
| Set Firefox as the default browser | ⚠️ once, by hand | ⚠️ once, by hand | detect drift, then nudge |
| Install packages | `winget` | `brew` | idempotent |
| Manage files / config | ✅ | ✅ | atomic writes |
| Ensure the daily account is not an admin | ✅ | ✅ | `user.account` |
| Report TCC consent state | — | ✅ | `macos.consent` |
| Inventory + compliance | ✅ | ✅ | poll every 5 min |

No Apple Developer Program, no Intune, no per-device fees.

### What it deliberately does not do

- **No remote shell.** The wire format only carries declarative policy over a
  fixed module set. A compromised server cannot run code as root on your fleet.
- **No "force" default browser.** Windows 11's `UCPD.sys` denies programmatic
  changes to the `UserChoice` keys even to SYSTEM; macOS keeps it in per-user
  session state. The agent detects drift and asks a human to click. See
  `docs/design.md` §2.2.
- **No TCC self-granting.** Consent is a one-time GUI step during provisioning;
  the agent reports whether it is still in place.

## Quick start

```bash
# 1. build
make build

# 2. run the control plane
export FLEET_ADMIN_TOKEN=$(openssl rand -hex 24)
./bin/fleet-server -addr :8080 -db fleet.db -key fleet-signing.key
```

```bash
# 3. mint an enrolment token (single use, 1 hour)
curl -s -X POST localhost:8080/api/v1/admin/enrollment-tokens \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"label":"mom-laptop","ttl_seconds":3600,"max_uses":1}'
```

```bash
# 4. define the policy for a group
jq '{policies: .policies}' examples/parents-policy.json > /tmp/p.json
curl -s -X PUT localhost:8080/api/v1/admin/groups/parents \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d @/tmp/p.json
```

```bash
# 5. on the managed machine
sudo ./bin/fleet-agent enroll -server https://fleet.example.com -token <TOKEN>
sudo ./bin/fleet-agent run          # or install it as a service, see below
```

```bash
# 6. put the machine in the group and watch it converge
curl -s -X PUT localhost:8080/api/v1/admin/devices/<device_id> \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" \
  -d '{"group":"parents"}'
curl -s localhost:8080/api/v1/admin/devices \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" | jq
```

Install as a service:

```bash
sudo scripts/install-unix.sh -b ./bin/fleet-agent -s https://fleet.example.com -t <TOKEN>
```

```powershell
.\scripts\install-windows.ps1 -Binary .\fleet-agent.exe -Server https://fleet.example.com -Token <TOKEN>
```

## Policy model

A policy item is `{id, type, class, enabled, config}`. The **class** is what keeps
the agent honest about what it can actually do:

| class | meaning |
|---|---|
| `enforce` | root/SYSTEM tier controls it; drift is reverted |
| `converge_user` | applied by the per-user session tier |
| `nudge` | cannot be applied programmatically; detect + prompt |
| `bootstrap_once` | needs a human GUI step; reported, never faked |
| `report_only` | observe, never change |

Policy is resolved per group with per-device overrides, then signed with Ed25519
and delivered as a generation-numbered bundle. The agent pins the server key on
first enrolment and refuses anything unsigned, unverifiable, or older than what
it already applied.

See [`examples/parents-policy.json`](examples/parents-policy.json).

## Architecture

```
   server (Go)                     agent (Go, root/SYSTEM)
   ├─ HTTP API            ◀──poll── ├─ verify signature, reject stale generation
   ├─ SQLite store                 ├─ apply modules (idempotent)
   ├─ Ed25519 signing              ├─ verify the observed result
   └─ audit log           ──report─▶└─ report status per item
```

Agents only dial **out** (no open ports, works behind NAT). Policy is polled
every 5 minutes with jitter.

## Security posture

- No command-execution endpoint; policy is a fixed module set.
- Signed bundles; the signing key never leaves the server.
- Strictly increasing bundle generation (anti-rollback).
- Device secrets are stored hashed; each device can only fetch its own policy.
- Every admin action is appended to the audit log.
- The admin API is disabled entirely when `FLEET_ADMIN_TOKEN` is unset.

Put the server behind TLS (reverse proxy or a mesh VPN such as Tailscale). The
signature is what protects policy integrity; TLS protects device secrets.

## Layout

```
cmd/server        control plane binary
cmd/agent         agent binary
internal/protocol wire types + bundle signing
internal/policy   class/tier vocabulary, validation, merge
internal/agent    enrol, poll, apply, report
internal/agent/modules  firefox, packages, file, browser.default,
                        macos.consent, user.account, inventory
internal/server   HTTP API, SQLite store
deploy/           launchd, systemd, windows install
scripts/          install + end-to-end test
docs/             design, provisioning
```

## Development

```bash
make help        # list targets
make test        # unit tests (all platforms in CI)
make e2e         # real server + real agent + real files
make vet fmt-check
make cross       # cross-compile the agent for all platforms
```

CI runs the unit tests on `ubuntu-latest`, `macos-latest` and `windows-latest`,
plus the logical end-to-end test, the real-OS end-to-end suite and a cross-compile
check. See [`test/e2e/README.md`](test/e2e/README.md) for what each tier proves.

## Status

Early. Working today: enrolment, signed policy bundles, change detection via
generation, Firefox policy, packages, files, account checks, browser-default
nudging, macOS consent reporting, inventory, audit log, service installers.

Next: signed agent self-update, drift detection on an interval independent of
policy generation, a minimal read-only web dashboard, and per-user (session tier)
execution of `converge_user` modules.
