#!/usr/bin/env bash
# One-time guest bootstrap for the macOS test VM. Run this INSIDE the VM, in
# Terminal, after the macOS installer has finished and you have logged in.
#
# It enables Remote Login (SSH), installs the host's public key and grants the
# login user passwordless sudo so the host can drive the test suite.
#
# This is a throwaway test VM: passwordless sudo is fine here and must never be
# used on a real machine.
#
#   bash /Volumes/shared/bootstrap-guest.sh
#
# The host generates this file (test/e2e/vm/macos/run.sh) with the public key
# already substituted. If you are pasting it by hand, replace __PUBKEY__.

set -euo pipefail

LOGIN_USER="${1:-$(id -un)}"
PUBKEY='__PUBKEY__'

echo "==> enabling Remote Login"
sudo systemsetup -setremotelogin on

echo "==> installing the host public key for $LOGIN_USER"
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
grep -qF "$PUBKEY" "$HOME/.ssh/authorized_keys" 2>/dev/null || echo "$PUBKEY" >> "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"

echo "==> granting passwordless sudo to $LOGIN_USER (test VM only)"
echo "$LOGIN_USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/fleet-e2e >/dev/null
sudo chmod 440 /etc/sudoers.d/fleet-e2e

echo "==> installing the fleet agent stub path"
sudo mkdir -p /usr/local/bin

echo
echo "ready. from the host: test/e2e/vm/macos/run.sh"
