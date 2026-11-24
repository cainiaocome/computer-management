#!/usr/bin/env bash
# The macOS side of the end-to-end suite. Run INSIDE the guest as root.
#
#   sudo SERVER=http://host.lan:8080 ADMIN_TOKEN=... test/e2e/guest/verify.sh
#
# Every assertion touches the real OS: real local accounts, real launchd, real
# /Applications, real TCC state. Nothing is mocked and nothing uses a filesystem
# root override.
#
# No python/jq dependency on purpose: a bare macOS VM has neither.

set -uo pipefail

SERVER="${SERVER:-http://host.lan:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-e2e-vm-admin-token}"
AGENT="${AGENT:-/usr/local/bin/fleet-agent}"
AGENT_SRC="${AGENT_SRC:-}"
INSTALLER="${INSTALLER:-}"
GROUP="${GROUP:-e2e-vm}"
STD_USER="${STD_USER:-fleetstd}"
ADM_USER="${ADM_USER:-fleetadm}"
USER_PASSWORD="${USER_PASSWORD:-E2e-Passw0rd!x}"
CFG="${CFG:-/Library/Application Support/fleet/agent.json}"
POLICY="/Applications/Firefox.app/Contents/Resources/distribution/policies.json"
WORK="$(mktemp -d)"
FAILED=0
TOTAL=0

pass() { TOTAL=$((TOTAL + 1)); printf '  \033[32mPASS\033[0m  %s\n' "$*"; }
fail() { TOTAL=$((TOTAL + 1)); FAILED=$((FAILED + 1)); printf '  \033[31mFAIL\033[0m  %s\n' "$*"; }
check() { # check <name> <condition-exit-code> [detail]
  if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1${3:+ - $3}"; fi
}
section() { printf '\n\033[36m--- %s\033[0m\n' "$*"; }
note() { printf '  \033[2m%s\033[0m\n' "$*"; }

api() { # api METHOD PATH [BODY]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$SERVER$path" \
      -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -d "$body"
  else
    curl -fsS -X "$method" "$SERVER$path" -H "Authorization: Bearer $ADMIN_TOKEN"
  fi
}

# status_of <reports-json> <id>
# The reports array is compact JSON with "id" and "status" in a fixed order, so
# splitting on '}' and grabbing the line that carries the id is stable enough,
# and needs no JSON parser on a bare macOS.
status_of() {
  printf '%s' "$1" | tr '}' '\n' | grep -F "\"id\":\"$2\"" | \
    sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p' | head -1
}

json_str() { # json_str <file> <key>
  sed -n 's/.*"'"$2"'":[[:space:]]*"\([^"]*\)".*/\1/p' "$1" | head -1
}

mtime_of() { stat -f %m "$1" 2>/dev/null || echo 0; }

# --- 0. preconditions ------------------------------------------------------
section "preconditions"
[ "$(id -u)" -eq 0 ]; check "running as root" $?
[ -x "$AGENT" ]; check "agent installed at $AGENT" $? "$AGENT"
note "guest: $(sw_vers -productName 2>/dev/null) $(sw_vers -productVersion 2>/dev/null) $(uname -m)"

curl -fsS --max-time 10 "$SERVER/healthz" >/dev/null 2>&1
check "server reachable at $SERVER" $?

# TCC state is reported, not granted: without MDM there is no API to grant it.
note "SIP: $(csrutil status 2>/dev/null | tr -s ' ')"
note "console user: $(stat -f %Su /dev/console 2>/dev/null)"

# --- 1. clean slate --------------------------------------------------------
section "clean slate"
rm -f "$POLICY" "$CFG"
for u in "$STD_USER" "$ADM_USER"; do
  if id "$u" >/dev/null 2>&1; then
    dscl . -delete "/Users/$u" >/dev/null 2>&1
  fi
done

make_user() { # make_user <name> <uid> <admin:0|1>
  local name="$1" uid="$2" isadmin="$3"
  if sysadminctl -addUser "$name" -fullName "Fleet $name" -password "$USER_PASSWORD" >/dev/null 2>&1; then
    :
  else
    # Fallback for guests where sysadminctl needs a GUI admin session.
    dscl . -create "/Users/$name"
    dscl . -create "/Users/$name" RealName "Fleet $name"
    dscl . -create "/Users/$name" UniqueID "$uid"
    dscl . -create "/Users/$name" PrimaryGroupID 20
    dscl . -create "/Users/$name" UserShell /bin/bash
    dscl . -create "/Users/$name" NFSHomeDirectory "/Users/$name"
    dscl . -passwd "/Users/$name" "$USER_PASSWORD"
    mkdir -p "/Users/$name"
    chown "$uid":20 "/Users/$name"
  fi
  if [ "$isadmin" = "1" ]; then
    dseditgroup -o edit -a "$name" -t user admin >/dev/null 2>&1
  else
    dseditgroup -o edit -d "$name" -t user admin >/dev/null 2>&1
  fi
}

make_user "$STD_USER" 1501 0
make_user "$ADM_USER" 1502 1
id "$STD_USER" >/dev/null 2>&1; check "standard user created" $?
id "$ADM_USER" >/dev/null 2>&1; check "admin user created" $?

# --- 2. enrolment ----------------------------------------------------------
section "enrolment"
TOKEN=$(api POST /api/v1/admin/enrollment-tokens \
  '{"label":"e2e-macos","ttl_seconds":900,"max_uses":1}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ]; check "enrolment token minted" $?

"$AGENT" enroll -config "$CFG" -server "$SERVER" -token "$TOKEN" -log-level error
check "enroll exits 0" $?
[ -f "$CFG" ]; check "config written" $? "$CFG"

DEVICE_ID=$(json_str "$CFG" device_id)
[ -n "$DEVICE_ID" ]; check "device id assigned" $? "$DEVICE_ID"
json_str "$CFG" signing_key | grep -q . ; check "signing key pinned" $?

"$AGENT" enroll -config "$CFG.replay" -server "$SERVER" -token "$TOKEN" -log-level error >/dev/null 2>&1
[ $? -ne 0 ] || [ ! -f "$CFG.replay" ]
check "replayed enrolment token is rejected" $?

# --- 3. policy -------------------------------------------------------------
section "policy assignment"
cat > "$WORK/policy.json" <<JSON
{"policies":[
 {"id":"firefox","type":"firefox.policy","class":"enforce","enabled":true,"config":{
   "homepage":"https://example.com","block_other_extensions":true,
   "extensions":[{"id":"uBlock0@raymondhill.net","url":"https://addons.mozilla.org/firefox/downloads/latest/ublock-origin/latest.xpi"}]}},
 {"id":"std-user","type":"user.account","class":"enforce","enabled":true,
  "config":{"username":"$STD_USER","ensure_standard":true}},
 {"id":"adm-user","type":"user.account","class":"enforce","enabled":true,
  "config":{"username":"$ADM_USER","ensure_standard":true}},
 {"id":"browser","type":"browser.default","class":"nudge","enabled":true,
  "config":{"browser":"firefox"}},
 {"id":"consent","type":"macos.consent","class":"bootstrap_once","enabled":true,
  "config":{"require":["app_management"]}},
 {"id":"inventory","type":"inventory","class":"report_only","enabled":true,"config":{}}
]}
JSON
api PUT "/api/v1/admin/groups/$GROUP" "$(cat "$WORK/policy.json")" >/dev/null
check "group policy accepted" $? "$GROUP"
api PUT "/api/v1/admin/devices/$DEVICE_ID" "{\"group\":\"$GROUP\"}" >/dev/null
check "device assigned to group" $?

# --- 4. apply --------------------------------------------------------------
section "apply"
REPORTS=$("$AGENT" once -config "$CFG" -json -log-level error 2>"$WORK/once.err")
RC=$?
check "apply exits 0" $RC "$(cat "$WORK/once.err")"
printf '%s' "$REPORTS" > "$WORK/reports.json"
COUNT=$(printf '%s' "$REPORTS" | tr ',' '\n' | grep -c '"id":"')
check "reports returned for every policy item" $([ "$COUNT" -eq 6 ] && echo 0 || echo 1) "$COUNT reports"

note "observed statuses:"
for id in firefox std-user adm-user browser consent inventory; do
  note "  $(printf '%-10s %s' "$id" "$(status_of "$REPORTS" "$id")")"
done

# --- 5. assertions against the real OS -------------------------------------
section "firefox policy on disk"
FF_STATUS=$(status_of "$REPORTS" firefox)
if [ -f "$POLICY" ]; then
  grep -q '"force_installed"' "$POLICY"; check "extension is force_installed" $?
  grep -q '"\*"' "$POLICY"; check "wildcard blocks other extensions" $?
  OWNER=$(stat -f %Su "$POLICY"); [ "$OWNER" = "root" ]; check "policy file owned by root" $? "$OWNER"
  case "$FF_STATUS" in changed|compliant) pass "firefox report is honest (status matches disk)";; *) fail "firefox report is honest (status matches disk) - $FF_STATUS";; esac
else
  # App Management consent is required on macOS 13+ to write inside another
  # app's bundle; claiming success without the file would be a lie.
  [ "$FF_STATUS" = "action_required" ]; check "missing policy reported as action_required, not success" $? "$FF_STATUS"
fi

section "account state matches reality"
dseditgroup -o checkmember -m "$STD_USER" admin >/dev/null 2>&1
[ $? -ne 0 ]; check "standard user is not an administrator" $?
dseditgroup -o checkmember -m "$ADM_USER" admin >/dev/null 2>&1
check "admin user really is an administrator" $?
[ "$(status_of "$REPORTS" std-user)" = "compliant" ]; check "user.account reports the standard user compliant" $? "$(status_of "$REPORTS" std-user)"
[ "$(status_of "$REPORTS" adm-user)" = "drifted" ]; check "user.account reports the admin user drifted" $? "$(status_of "$REPORTS" adm-user)"

section "consent is reported, never faked"
CONSENT=$(status_of "$REPORTS" consent)
case "$CONSENT" in
  action_required) [ ! -f "$POLICY" ] || true; pass "consent reported as action_required";;
  compliant) [ -f "$POLICY" ]; check "consent claims compliant only when App Management is really granted" $? "$CONSENT";;
  *) fail "consent reported as action_required or compliant - $CONSENT";;
esac

section "default browser is never silently changed"
BR=$(status_of "$REPORTS" browser)
[ "$BR" != "changed" ]; check "browser.default never claims it changed anything" $? "$BR"
case "$BR" in action_required|compliant) pass "browser.default gives the user an action";; *) fail "browser.default gives the user an action - $BR";; esac

# --- 6. idempotency --------------------------------------------------------
section "idempotency"
MTIME_BEFORE=$(mtime_of "$POLICY")
GEN_BEFORE=$(json_str "$CFG" generation)
"$AGENT" once -config "$CFG" -log-level error >/dev/null 2>&1
sleep 1
GEN_AFTER=$(json_str "$CFG" generation)
MTIME_AFTER=$(mtime_of "$POLICY")
[ "$GEN_BEFORE" = "$GEN_AFTER" ]; check "generation unchanged without a policy change" $? "$GEN_BEFORE -> $GEN_AFTER"
[ "$MTIME_BEFORE" = "$MTIME_AFTER" ]; check "policy file not rewritten on a no-op cycle" $? "mtime changed"

# --- 7. launchd ------------------------------------------------------------
section "launchd"
if [ -n "${INSTALLER:-}" ] && [ -f "$INSTALLER" ]; then
  bash "$INSTALLER" -b "$AGENT" >/dev/null 2>&1
  check "installer exits 0" $?
  bash "$INSTALLER" -b "$AGENT" >/dev/null 2>&1
  check "installer is idempotent (second run)" $?
  sleep 2
  launchctl print system/com.fleet.agent >/dev/null 2>&1
  check "launchd job is loaded" $?
  launchctl print system/com.fleet.agent 2>/dev/null | grep -q 'state = running'
  check "launchd job is running" $?

  launchctl bootout system/com.fleet.agent >/dev/null 2>&1
  rm -f /Library/LaunchDaemons/com.fleet.agent.plist
  sleep 1
  launchctl print system/com.fleet.agent >/dev/null 2>&1
  [ $? -ne 0 ]; check "job unloads cleanly" $?
else
  note "installer not staged (INSTALLER unset); skipping launchd checks"
fi

# --- 8. code identity -------------------------------------------------------
section "code identity"
# macOS binds TCC grants to the *code identity* (the designated requirement), not
# to a path. For an ad-hoc signature that requirement is the cdhash, so any
# rebuild re-prompts for every grant - which is why docs/provisioning.md insists
# on one stable signing identity. The mechanism is provable without setting up a
# trusted certificate (that recipe lives in the provisioning doc).
if command -v codesign >/dev/null 2>&1; then
  cp "$AGENT" "$WORK/a1"
  cp "$AGENT" "$WORK/a2"
  codesign -f -s - -i com.fleet.e2e.agent "$WORK/a1" >/dev/null 2>&1
  check "ad-hoc signing works" $?
  codesign --verify --strict "$WORK/a1" >/dev/null 2>&1
  check "ad-hoc signature verifies" $?

  codesign -f -s - -i com.fleet.e2e.agent "$WORK/a2" >/dev/null 2>&1
  H1=$(codesign -dv --verbose=4 "$WORK/a1" 2>&1 | sed -n 's/^CDHash=//p')
  H2=$(codesign -dv --verbose=4 "$WORK/a2" 2>&1 | sed -n 's/^CDHash=//p')
  [ -n "$H1" ] && [ "$H1" = "$H2" ]
  check "identical content signs to an identical code directory hash" $? "$H1 vs $H2"

  # One changed byte inside the signed range is exactly what a rebuild does.
  # (Appending to the file would *not* work: cdhash only covers up to codeLimit,
  # which is why trailing bytes do not change the identity.) The identifier is
  # pinned above on purpose: codesign otherwise derives it from the file name.
  cp "$AGENT" "$WORK/a3"
  printf '\102' | dd of="$WORK/a3" bs=1 seek=65536 conv=notrunc 2>/dev/null
  codesign -f -s - -i com.fleet.e2e.agent "$WORK/a3" >/dev/null 2>&1
  H3=$(codesign -dv --verbose=4 "$WORK/a3" 2>&1 | sed -n 's/^CDHash=//p')
  [ -n "$H3" ] && [ "$H3" != "$H1" ]
  check "a change inside the signed range produces a new identity" $? "$H3"

  DR=$(codesign -d -r- "$WORK/a1" 2>&1)
  case "$DR" in
    *cdhash*) pass "ad-hoc designated requirement is keyed to the cdhash";;
    *) fail "ad-hoc designated requirement is keyed to the cdhash - $DR";;
  esac
  # GitHub-hosted runners ship with SIP disabled, so this layer proves the
  # mechanics of code identity, not SIP/TCC enforcement itself.
  note "SIP: $(csrutil status 2>/dev/null | tr -s ' ')"
else
  note "codesign not available; skipping code identity checks"
fi

# --- summary ---------------------------------------------------------------
PASSED=$((TOTAL - FAILED))
printf '\n\033[36m===== %d/%d checks passed =====\033[0m\n' "$PASSED" "$TOTAL"
[ "$FAILED" -eq 0 ]
