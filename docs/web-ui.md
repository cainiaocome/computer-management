# The web UI

English | [简体中文](./web-ui.zh-CN.md)

The control plane ships with a small web UI. It is the same binary, the same
database and the same policy objects as the JSON admin API: the UI is a second
view of the control plane, not a second control plane.

## Why it lives on a different listener

Agents have to reach the server from wherever the laptops are, so `FLEET_ADDR`
(the default `:8080`) is often exposed. The admin API and the UI are not: they
can change every machine's policy, so they listen on `FLEET_ADMIN_ADDR`
(default `127.0.0.1:8081`) instead.

```bash
FLEET_ADMIN_TOKEN=$(openssl rand -hex 24) \
  ./fleet-server -addr :8080 -admin-addr 127.0.0.1:8081
# UI:    http://127.0.0.1:8081/ui/
# agents: http://<host>:8080
```

Reach it over SSH port-forwarding or Tailscale. If you set
`FLEET_ADMIN_ADDR=""` or equal to `FLEET_ADDR`, both are served on one port and
the server logs a warning — that is the pre-1.0 layout, kept for compatibility,
not the recommendation.

## Signing in

The admin token is the only credential. It is exchanged for a session cookie and
never stored in the browser:

- `HttpOnly`, so JavaScript cannot read it
- `SameSite=Strict`, so another site cannot cause a state-changing request
- `Secure` automatically when the request arrived over TLS
- 12-hour expiry, HMAC-signed with a key derived from the admin token, so
  rotating the token signs every session out

State-changing form posts are additionally checked against the `Origin` header.

## What it does

| Page | Purpose |
|---|---|
| **Devices** | every enrolled machine, its group, policy generation, last checkin, and a per-status count. `attention` means at least one item is `drifted`, `action_required` or `error`; `stale` means no report, or no checkin for 7 days |
| **Device** | the last report per policy item, with the detail JSON; group assignment; per-device override JSON, validated before it is stored; **revoke**, which deletes the device so its secret stops working at the next checkin |
| **Groups** | the policy bundles, editable as JSON with the same `policy.Validate` checks the API applies (unknown type, a class that does not fit the type's execution tier, an enabled item with no config) |
| **Enrolment** | mint a token with a label, TTL and use count. The secret is displayed exactly once: only a SHA-256 hash is stored |
| **Audit** | the last 200 entries: logins, policy changes, token creation, revocations |

Everything the UI does is an existing control-plane operation, so it appears in
the audit log with actor `ui` and is visible whether it came from the UI or the
API.

## What it deliberately does not do

- **No command execution.** There is no "run script" button, no console, no file
  browser. The wire protocol only carries declarative policy items; a UI that
  could run a command would recreate the exact failure mode the design exists to
  avoid (see `design.md` §7.1 for what a compromised server can still do).
- **No "apply now".** Saving a policy bumps the generation; each device picks it
  up at its next checkin. Anything else would need a push channel the agent does
  not have.
- **Revoke is not wipe.** It removes the device record so the secret stops
  working. The machine keeps its current state.
- **No multi-user.** There is one admin identity. There are no roles, no 2FA and
  no per-group admins; that is a deliberate non-goal for a family fleet.

## Implementation notes

Server-rendered HTML (`html/template`) with a small stylesheet, embedded with
`//go:embed`. No build step, no npm, no JavaScript framework, and no inline
script — the CSP is `default-src 'none'; style-src 'self'; form-action 'self'`,
so an injected string in a hostname or policy message cannot execute.

A separate SPA was considered and rejected: it would need CORS on the admin API,
a token in browser storage, a second artefact to deploy and a build toolchain,
for a UI with five tables and two textareas.

Files:

```
internal/server/session.go              cookie signing, constant-time token check, Origin guard
internal/server/webui.go                handlers, view models, template registry
internal/server/webui/templates/*.html  one file per page, plus layout
internal/server/webui/static/app.css    the whole stylesheet
internal/server/webui_test.go           auth, CSRF, validation, one-time token display
```

## Limits worth knowing

- No CSRF token: `SameSite=Strict` plus the `Origin` check covers the browser
  cases; a token would only add defence against a subdomain of the same site.
- The UI trusts the reverse proxy for TLS. Terminate TLS in front of it, or use
  an SSH tunnel.
- A policy item's `detail` JSON is rendered escaped, so it is safe but not
  pretty-printed for huge reports.
