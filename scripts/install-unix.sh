#!/usr/bin/env bash
# Install the fleet agent on macOS or Linux, then enrol it.
#
#   sudo scripts/install-unix.sh -b ./bin/fleet-agent -s https://fleet.example.com -t TOKEN
#
# Installing via curl/ssh (rather than a browser download) means the binary never
# gets the com.apple.quarantine attribute, so Gatekeeper does not block it.
set -euo pipefail

BIN=""
SERVER=""
TOKEN=""

while getopts "b:s:t:h" opt; do
  case "$opt" in
    b) BIN="$OPTARG" ;;
    s) SERVER="$OPTARG" ;;
    t) TOKEN="$OPTARG" ;;
    h) sed -n '2,9p' "$0"; exit 0 ;;
    *) exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root (sudo)" >&2
  exit 1
fi
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "usage: $0 -b /path/to/fleet-agent [-s SERVER -t TOKEN]" >&2
  exit 2
fi

install -d -m 0755 /usr/local/bin
install -m 0755 "$BIN" /usr/local/bin/fleet-agent

case "$(uname -s)" in
  Darwin)
    # Signing note: TCC grants bind to the code identity. If you replace the
    # binary with a differently-signed build, macOS will re-prompt for
    # App Management / Full Disk Access. Keep one stable signing identity.
    xattr -d com.apple.quarantine /usr/local/bin/fleet-agent 2>/dev/null || true
    install -m 0644 deploy/launchd/com.fleet.agent.plist /Library/LaunchDaemons/com.fleet.agent.plist
    chown root:wheel /Library/LaunchDaemons/com.fleet.agent.plist
    launchctl bootout system/com.fleet.agent 2>/dev/null || true
    launchctl bootstrap system /Library/LaunchDaemons/com.fleet.agent.plist
    ;;
  Linux)
    install -m 0644 deploy/systemd/fleet-agent.service /etc/systemd/system/fleet-agent.service
    systemctl daemon-reload
    systemctl enable --now fleet-agent.service
    ;;
  *)
    echo "unsupported OS: $(uname -s)" >&2
    exit 1
    ;;
esac

if [ -n "$SERVER" ] && [ -n "$TOKEN" ]; then
  fleet-agent enroll -server "$SERVER" -token "$TOKEN"
fi

echo "installed. config: $(fleet-agent status 2>/dev/null || echo 'not enrolled yet')"
