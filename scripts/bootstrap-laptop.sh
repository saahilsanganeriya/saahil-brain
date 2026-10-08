#!/bin/bash
# Run ONCE on the laptop:   ssh devbox-host cat /home/test-user/.local/share/brain/dist/bootstrap-laptop.sh | bash
# Installs ~/.local/bin/brain (checked by sha256), copies the bridge token from the devbox, loads the bridge and its
# reverse tunnel as LaunchAgents, then asks the devbox to call back through the bridge. Later updates arrive with
# `brain self-update` (the devbox asks for it through the bridge); this script is not needed again.
set -euo pipefail
host=devbox-host
dist=/home/test-user/.local/share/brain/dist
ssh_opts=(-o BatchMode=yes -o ControlPath=none -o ConnectTimeout=10)
mkdir -p "$HOME/.local/bin" "$HOME/.local/state/brain"
mkdir -p -m 700 "$HOME/.config/brain"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
scp -q "${ssh_opts[@]}" "$host:$dist/brain-darwin-arm64" "$tmp/brain"
scp -q "${ssh_opts[@]}" "$host:$dist/brain-darwin-arm64.sha256" "$tmp/brain.sha256"
want=$(awk '{print $1}' "$tmp/brain.sha256")
got=$(shasum -a 256 "$tmp/brain" | awk '{print $1}')
[ "$want" = "$got" ] || { echo "sha256 mismatch: got $got want $want" >&2; exit 1; }
install -m 755 "$tmp/brain" "$HOME/.local/bin/brain"
(umask 077; ssh "${ssh_opts[@]}" "$host" 'cat ~/.config/brain/bridge.token' > "$HOME/.config/brain/bridge.token")
"$HOME/.local/bin/brain" version
"$HOME/.local/bin/brain" bridge install
echo "waiting for the tunnel…"
for i in $(seq 1 20); do
  if ssh "${ssh_opts[@]}" "$host" '/home/test-user/.local/bin/brain peer status' >/dev/null 2>&1; then
    echo "BRIDGE OK: the devbox reaches this laptop through the bridge"
    ssh "${ssh_opts[@]}" "$host" '/home/test-user/.local/bin/brain agents list'
    exit 0
  fi
  sleep 3
done
echo "the devbox could not reach the bridge yet; see ~/Library/Logs/brain-tunnel.log and brain-bridge.log" >&2
exit 1
