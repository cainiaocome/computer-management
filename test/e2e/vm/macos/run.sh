#!/usr/bin/env bash
# Run the end-to-end suite against a real macOS guest over SSH.
#
# First time (one-time manual work, because the macOS installer is interactive):
#
#   1. test/e2e/vm/macos/run.sh --prepare
#      -> boots the VM and prints the web console URL
#   2. In the console: Disk Utility -> erase the virtio disk -> reinstall macOS,
#      create your account, finish setup.
#   3. Mount the shared folder and run the bootstrap:
#         sudo -S mount_9p shared   ||  sudo mount -t 9p -o trans=virtio shared /Volumes
#         bash /Volumes/shared/bootstrap-guest.sh
#   4. test/e2e/vm/macos/run.sh
#
# Afterwards every run is unattended.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../../.." && pwd)"
LOGS="$HERE/logs"
SSH_KEY="$HERE/.ssh/id_ed25519"
SSH_PORT="${SSH_PORT:-2223}"
FLEET_PORT="${FLEET_PORT:-8080}"
ADMIN_TOKEN="${FLEET_ADMIN_TOKEN:-e2e-vm-admin-token}"
MACOS_USER="${MACOS_USER:-admin}"
MACOS_ARCH="${MACOS_ARCH:-amd64}"
WAIT_MINUTES="${WAIT_MINUTES:-30}"

say() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

mkdir -p "$LOGS" "$HERE/shared"

if [ "${RESET:-0}" = "1" ]; then
  say "RESET=1: destroying the macOS disk (you will have to reinstall)"
  (cd "$HERE" && docker compose down -v 2>/dev/null || true)
  rm -rf "$HERE/storage"
fi

if [ ! -f "$SSH_KEY" ]; then
  say "generating an ssh key for the guest"
  mkdir -p "$(dirname "$SSH_KEY")"
  ssh-keygen -t ed25519 -N '' -C 'fleet-e2e' -f "$SSH_KEY" >/dev/null
fi

# Generate the in-guest bootstrap with this run's public key baked in.
sed "s|__PUBKEY__|$(cat "$SSH_KEY.pub")|" "$HERE/bootstrap-guest.sh" > "$HERE/shared/bootstrap-guest.sh"
chmod +x "$HERE/shared/bootstrap-guest.sh"

# Build the macOS agent and stage what the guest needs.
say "building fleet-agent (darwin/$MACOS_ARCH)"
(cd "$REPO" && CGO_ENABLED=0 GOOS=darwin GOARCH="$MACOS_ARCH" \
  go build -trimpath -o "$HERE/shared/fleet-agent" ./cmd/agent)
cp "$REPO/test/e2e/guest/verify.sh" "$HERE/shared/"
# install-unix.sh resolves deploy/ relative to its own parent, so stage the tree.
mkdir -p "$HERE/shared/stage/scripts" "$HERE/shared/stage/deploy"
cp "$REPO/scripts/install-unix.sh" "$HERE/shared/stage/scripts/"
cp -R "$REPO/deploy/launchd" "$HERE/shared/stage/deploy/"

if [ "${1:-}" = "--prepare" ]; then
  say "booting the macOS VM (the installer needs you in the web console)"
  (cd "$HERE" && docker compose up -d)
  cat <<EOF

  Open http://127.0.0.1:${WEB_PORT:-8007} and complete the installation:

    1. Disk Utility -> select the Apple VirtIO disk -> Erase (APFS)
    2. Reinstall macOS, choose that disk, finish setup, create your account
    3. In Terminal (inside the VM):
         sudo -S mount_9p shared || true
         bash /Volumes/shared/bootstrap-guest.sh

  Then re-run: $0
EOF
  exit 0
fi

say "starting the fleet server on :$FLEET_PORT"
(cd "$REPO" && CGO_ENABLED=0 go build -trimpath -o "$REPO/bin/fleet-server" ./cmd/server)
"$REPO/bin/fleet-server" -addr "0.0.0.0:$FLEET_PORT" \
  -db "$LOGS/fleet.db" -key "$LOGS/fleet-signing.key" \
  -admin-token "$ADMIN_TOKEN" -log-level warn >"$LOGS/server.log" 2>&1 &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null || true' EXIT
sleep 1

say "booting the VM"
(cd "$HERE" && docker compose up -d)

ssh_guest() {
  ssh -i "$SSH_KEY" -p "$SSH_PORT" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=10 -o BatchMode=yes \
    "$MACOS_USER@127.0.0.1" "$@"
}

say "waiting for SSH on 127.0.0.1:$SSH_PORT (timeout ${WAIT_MINUTES}m)"
deadline=$(( $(date +%s) + WAIT_MINUTES * 60 ))
until ssh_guest 'echo ready' >/dev/null 2>&1; do
  if [ "$(date +%s)" -gt "$deadline" ]; then
    cat <<EOF >&2

SSH never came up. If this is the first run, do the one-time install:
  $0 --prepare
then run bootstrap-guest.sh inside the VM (see the header of this file).
EOF
    exit 1
  fi
  printf '.'
  sleep 10
done
echo
say "guest is reachable"

say "staging the agent and the suite"
scp -q -i "$SSH_KEY" -P "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "$HERE/shared/fleet-agent" "$MACOS_USER@127.0.0.1:/tmp/fleet-agent"
scp -q -i "$SSH_KEY" -P "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -r "$HERE/shared/stage" "$HERE/shared/verify.sh" "$MACOS_USER@127.0.0.1:/tmp/"
ssh_guest 'sudo install -m 0755 /tmp/fleet-agent /usr/local/bin/fleet-agent && sudo chown root:wheel /usr/local/bin/fleet-agent'

say "running the end-to-end suite inside macOS"
set +e
# Forward the server port over this SSH connection (-R) so the guest does not
# depend on being able to route to the host.
ssh -i "$SSH_KEY" -p "$SSH_PORT" \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -o LogLevel=ERROR -o ConnectTimeout=10 -o BatchMode=yes \
  -R "$FLEET_PORT:127.0.0.1:$FLEET_PORT" \
  "$MACOS_USER@127.0.0.1" \
  "sudo env SERVER=http://127.0.0.1:$FLEET_PORT ADMIN_TOKEN=$ADMIN_TOKEN \
   AGENT=/usr/local/bin/fleet-agent \
   INSTALLER=/tmp/stage/scripts/install-unix.sh \
   bash /tmp/verify.sh" 2>&1 | tee "$LOGS/verify.log"
RC=${PIPESTATUS[0]}
set -e

[ "$RC" -eq 0 ] || die "guest suite failed (see $LOGS/verify.log)"
say "macOS end-to-end suite passed"
