#!/usr/bin/env bash
# End-to-end smoke test: real server, real agent binary, real HTTP, real files.
#
# It proves the whole loop works: admin mints an enrolment token, the agent
# enrols, the server signs a bundle, the agent verifies the signature, applies
# it to a scratch root and reports compliance.
set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${PORT:-18080}"
ADMIN_PORT="${ADMIN_PORT:-18081}"
BASE="http://127.0.0.1:${PORT}"
ADMIN="http://127.0.0.1:${ADMIN_PORT}"
WORK="$(mktemp -d)"
ADMIN_TOKEN="e2e-admin-token"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

say() { printf '\033[36m==> %s\033[0m\n' "$*"; }

say "building"
mkdir -p "$WORK/bin"
CGO_ENABLED=0 go build -o "$WORK/bin/fleet-server" ./cmd/server
CGO_ENABLED=0 go build -o "$WORK/bin/fleet-agent" ./cmd/agent

say "starting server on ${BASE} (admin and UI on ${ADMIN})"
# Same layout as production: agents on one port, admin API and web UI on another.
"$WORK/bin/fleet-server" -addr "127.0.0.1:${PORT}" -admin-addr "127.0.0.1:${ADMIN_PORT}" \
  -db "$WORK/fleet.db" -key "$WORK/signing.key" \
  -admin-token "$ADMIN_TOKEN" -log-level warn >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
curl -fsS "$BASE/healthz" >/dev/null || { echo "server did not start"; cat "$WORK/server.log"; exit 1; }

say "agent port serves no admin surface"
if curl -fsS "$BASE/api/v1/admin/devices" >/dev/null 2>&1; then
  echo "FAIL: the admin API answered on the agent port"; exit 1
fi
if curl -fsS "$BASE/ui/" >/dev/null 2>&1; then
  echo "FAIL: the web UI answered on the agent port"; exit 1
fi
curl -fsS "$ADMIN/ui/login" >/dev/null || { echo "FAIL: the UI does not answer on the admin port"; exit 1; }
echo "   ok"

say "minting an enrolment token"
TOKEN=$(curl -fsS -X POST "$ADMIN/api/v1/admin/enrollment-tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"label":"e2e-mac","ttl_seconds":600,"max_uses":1}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
[ -n "$TOKEN" ] || { echo "no token"; exit 1; }

say "enrolling the agent"
"$WORK/bin/fleet-agent" enroll -config "$WORK/agent.json" -server "$BASE" -token "$TOKEN" -log-level warn
DEVICE_ID=$(python3 -c 'import json;print(json.load(open("'"$WORK"'/agent.json"))["device_id"])')

say "assigning the parents group policy"
python3 - "$WORK/policy.json" <<'PY'
import json, sys
policy = {"policies": [
  {"id": "firefox", "type": "firefox.policy", "class": "enforce", "enabled": True,
   "config": {
     "extensions": [{"id": "uBlock0@raymondhill.net",
                     "url": "https://addons.mozilla.org/firefox/downloads/latest/ublock-origin/latest.xpi"}],
     "homepage": "https://example.com",
     "block_other_extensions": True}},
  {"id": "browser-default", "type": "browser.default", "class": "nudge", "enabled": True,
   "config": {"browser": "firefox"}},
  {"id": "inventory", "type": "inventory", "class": "report_only", "enabled": True, "config": {}}
]}
json.dump(policy, open(sys.argv[1], "w"))
PY

curl -fsS -X PUT "$ADMIN/api/v1/admin/groups/parents" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d @"$WORK/policy.json" >/dev/null
curl -fsS -X PUT "$ADMIN/api/v1/admin/devices/$DEVICE_ID" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"group":"parents"}' >/dev/null

say "running one agent cycle (writing into a scratch root)"
mkdir -p "$WORK/root"
"$WORK/bin/fleet-agent" once -config "$WORK/agent.json" -root "$WORK/root" -log-level warn

case "$(uname -s)" in
  Darwin) POLICY_FILE="$WORK/root/Applications/Firefox.app/Contents/Resources/distribution/policies.json" ;;
  Linux)  POLICY_FILE="$WORK/root/etc/firefox/policies/policies.json" ;;
  *) echo "unsupported host for e2e"; exit 1 ;;
esac
[ -f "$POLICY_FILE" ] || { echo "FAIL: Firefox policy not written"; find "$WORK/root" -type f; exit 1; }
grep -q force_installed "$POLICY_FILE" || { echo "FAIL: policy does not force-install"; cat "$POLICY_FILE"; exit 1; }

say "second cycle must be a no-op (generation is stable)"
GEN1=$(python3 -c 'import json;print(json.load(open("'"$WORK"'/agent.json"))["generation"])')
"$WORK/bin/fleet-agent" once -config "$WORK/agent.json" -root "$WORK/root" -log-level warn
GEN2=$(python3 -c 'import json;print(json.load(open("'"$WORK"'/agent.json"))["generation"])')
[ "$GEN1" = "$GEN2" ] || { echo "FAIL: generation changed without a policy change ($GEN1 -> $GEN2)"; exit 1; }

say "server sees the device as compliant"
curl -fsS "$ADMIN/api/v1/admin/devices" -H "Authorization: Bearer $ADMIN_TOKEN" \
  | python3 -c '
import json, sys
devices = json.load(sys.stdin)["devices"]
assert len(devices) == 1, devices
d = devices[0]
print("   device:", d["hostname"], d["os"], "gen", d["generation"], "compliance", d["compliance"])
assert d["compliance"].get("compliant", 0) + d["compliance"].get("action_required", 0) >= 2, d
'

say "a replayed enrolment token is rejected"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/enroll" \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\",\"hostname\":\"evil\",\"os\":\"linux\",\"arch\":\"amd64\"}")
[ "$CODE" = "403" ] || { echo "FAIL: replay returned $CODE"; exit 1; }

printf '\033[32m==> e2e passed\033[0m\n'
