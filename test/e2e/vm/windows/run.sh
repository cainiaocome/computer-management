#!/usr/bin/env bash
# Boot a real Windows 11 VM and run the end-to-end suite inside it over SSH.
#
#   test/e2e/vm/windows/run.sh              # boot (or reuse) the VM and test
#   RESET=1 test/e2e/vm/windows/run.sh      # wipe the disk and reinstall Windows
#   DOWN=1  test/e2e/vm/windows/run.sh      # stop the VM when finished
#
# Everything the test asserts happens inside a real Windows guest: real registry,
# real local users, real services, real Program Files, real UCPD.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../../.." && pwd)"
LOGS="$HERE/logs"
SSH_KEY="$HERE/.ssh/id_ed25519"
SSH_PORT="${SSH_PORT:-2222}"
FLEET_PORT="${FLEET_PORT:-8080}"
WAIT_MINUTES="${WAIT_MINUTES:-90}"
ADMIN_TOKEN="${FLEET_ADMIN_TOKEN:-e2e-vm-admin-token}"
VM_USER="${WINDOWS_USER:-Docker}"

mkdir -p "$LOGS" "$HERE/shared" "$HERE/oem"

say() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

ssh_guest() {
  ssh -i "$SSH_KEY" -p "$SSH_PORT" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=10 -o BatchMode=yes \
    "$VM_USER@127.0.0.1" "$@"
}

# --- prerequisites ---------------------------------------------------------
[ -e /dev/kvm ] || die "/dev/kvm not available: this needs a Linux host with KVM"
docker compose version >/dev/null 2>&1 || die "docker compose plugin not found"

if [ "${RESET:-0}" = "1" ]; then
  say "RESET=1: destroying the VM disk"
  (cd "$HERE" && docker compose down -v 2>/dev/null || true)
  rm -rf "$HERE/storage"
fi

# --- ssh key ---------------------------------------------------------------
if [ ! -f "$SSH_KEY" ]; then
  say "generating an ssh key for the guest"
  mkdir -p "$(dirname "$SSH_KEY")"
  ssh-keygen -t ed25519 -N '' -C 'fleet-e2e' -f "$SSH_KEY" >/dev/null
fi
cp "$SSH_KEY.pub" "$HERE/oem/authorized_keys"

# OpenSSH for the guest, installed offline during the unattended install.
# Add-WindowsCapability would pull it from Windows Update and can hang for a
# long time in a fresh VM, so stage the release zip instead.
if [ ! -f "$HERE/oem/OpenSSH-Win64.zip" ]; then
  say "downloading OpenSSH-Win64.zip for the guest"
  curl -fsSL -o "$HERE/oem/OpenSSH-Win64.zip" \
    https://github.com/PowerShell/Win32-OpenSSH/releases/latest/download/OpenSSH-Win64.zip \
    || die "could not download OpenSSH-Win64.zip (stage it into oem/ manually)"
fi

# --- build artifacts the guest needs --------------------------------------
say "building fleet-agent.exe and staging it for the guest"
mkdir -p "$REPO/bin"
(cd "$REPO" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o "$HERE/shared/fleet-agent.exe" ./cmd/agent)
cp "$REPO/scripts/install-windows.ps1" "$HERE/shared/"
cp "$REPO/test/e2e/guest/verify.ps1" "$HERE/shared/"

# --- fleet server on the host ---------------------------------------------
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  if [ "${DOWN:-0}" = "1" ]; then (cd "$HERE" && docker compose down); fi
}
trap cleanup EXIT

say "starting the fleet server on :$FLEET_PORT"
(cd "$REPO" && CGO_ENABLED=0 go build -trimpath -o "$REPO/bin/fleet-server" ./cmd/server)
"$REPO/bin/fleet-server" -addr "0.0.0.0:$FLEET_PORT" \
  -db "$LOGS/fleet.db" -key "$LOGS/fleet-signing.key" \
  -admin-token "$ADMIN_TOKEN" -log-level warn >"$LOGS/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$FLEET_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS "http://127.0.0.1:$FLEET_PORT/healthz" >/dev/null \
  || die "server did not start; see $LOGS/server.log"

# --- boot the VM -----------------------------------------------------------
say "starting the Windows VM (first boot installs Windows, this is the slow part)"
(cd "$HERE" && docker compose up -d)

say "waiting for SSH on 127.0.0.1:$SSH_PORT (timeout ${WAIT_MINUTES}m)"
deadline=$(( $(date +%s) + WAIT_MINUTES * 60 ))
until ssh_guest 'echo ready' >/dev/null 2>&1; do
  if [ "$(date +%s)" -gt "$deadline" ]; then
    docker logs --tail 40 "${CONTAINER_NAME:-fleet-e2e-windows}" 2>&1 | tail -40 || true
    die "guest SSH never came up"
  fi
  printf '.'
  sleep 15
done
echo
say "guest is reachable"

# --- stage files over SSH --------------------------------------------------
# dockur's /shared mount is not reliably mapped inside the guest (it depends on
# the image's logon script), so copy what we need with scp instead of relying on
# a drive letter.
say "staging the agent and the suite in the guest"
ssh_guest 'powershell -NoProfile -Command "New-Item -ItemType Directory -Force C:\fleet-e2e | Out-Null"' >/dev/null
scp -q -i "$SSH_KEY" -P "$SSH_PORT" \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "$HERE/shared/fleet-agent.exe" "$HERE/shared/verify.ps1" "$HERE/shared/install-windows.ps1" \
  "$VM_USER@127.0.0.1:C:/fleet-e2e/"

# --- run the real suite inside the guest -----------------------------------
say "running the end-to-end suite inside Windows"
set +e
# The guest cannot route to the host directly (dockur's host.lan points at the
# container, and the published port is host-side only), so forward the server
# port over the same SSH connection with -R and talk to 127.0.0.1 in the guest.
ssh -i "$SSH_KEY" -p "$SSH_PORT" \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -o LogLevel=ERROR -o ConnectTimeout=10 -o BatchMode=yes \
  -R "$FLEET_PORT:127.0.0.1:$FLEET_PORT" \
  "$VM_USER@127.0.0.1" \
  "powershell -NoProfile -ExecutionPolicy Bypass -File C:\\fleet-e2e\\verify.ps1 -Server http://127.0.0.1:$FLEET_PORT -AdminToken $ADMIN_TOKEN -Agent C:\\fleet-e2e\\fleet-agent.exe -Installer C:\\fleet-e2e\\install-windows.ps1" 2>&1 | tee "$LOGS/verify.log"
RC=${PIPESTATUS[0]}
set -e

# PowerShell can exit 0 even when the script failed to start, so trust the
# suite's own summary line as well.
grep -q 'checks passed' "$LOGS/verify.log" || { echo "suite did not run to completion"; RC=1; }
if grep -q 'FAIL' "$LOGS/verify.log"; then RC=1; fi

ssh_guest 'powershell -NoProfile -Command "if (Test-Path C:\fleet-e2e-results.json) { Get-Content C:\fleet-e2e-results.json -Raw }"' >"$LOGS/results.json" 2>/dev/null || true

if [ "$RC" -ne 0 ]; then
  die "guest suite failed (see $LOGS/verify.log)"
fi
say "Windows end-to-end suite passed"
