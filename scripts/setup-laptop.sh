#!/bin/bash
# setup-laptop.sh <devbox-host> [--dry-run] [--force] — set up the brain on the laptop (macOS; AGENT-SETUP.md step 2),
# after setup-devbox.sh ran on the devbox. Everything comes over ssh from the devbox's ~/.local/share/brain/dist:
#
#   · ~/.local/bin/brain (sha256 checked) and ~/.local/bin/devbox (the EC2 helper)
#   · the bridge token and identity.json, copied from the devbox (one settings file, the same on both machines)
#   · brain bridge install (the bridge and its reverse tunnel, as LaunchAgents); unison and the hourly brain sync
#   · brain init; brain install (Claude Code mod + autoMemoryDirectory, Codex, Cursor, Prime, omp where present)
#   · then the devbox calls back through the bridge: `brain peer status`
#
# Each step prints what it did, or that it was already done. --dry-run prints every change and makes none. --force
# replaces a file that differs (identity.json, ~/.local/bin/devbox; a .bak is kept) and reinstalls the LaunchAgents.
# The brain binary, the bridge token and the dist files always follow the devbox: it is their source.
set -euo pipefail

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'setup-laptop: %s\n' "$*" >&2; exit 1; }

dry=0 force=0 host=""
while [ $# -gt 0 ]; do
  case $1 in
    --dry-run) dry=1 ;;
    --force) force=1 ;;
    -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d'; exit 0 ;;
    -*) die "unknown option: $1 (see --help)" ;;
    *) host=$1 ;;
  esac
  shift
done
[ -n "$host" ] || die "usage: setup-laptop.sh <devbox-host> [--dry-run] [--force]"

act() { if [ "$dry" = 1 ]; then say "  would: $*"; else "$@"; fi; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

# ssh and scp start in the devbox user's home, so these paths need no devbox username
DIST=.local/share/brain/dist
SSH_OPTS=(-o BatchMode=yes -o ControlPath=none -o ConnectTimeout=10)
brain=$HOME/.local/bin/brain
cfg=$HOME/.config/brain
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Same contract as setup-devbox.sh: identical bytes are left alone; a different file is replaced only with --force.
install_file() { # $1 src, $2 dst, $3 mode, $4 = always to replace a different file without --force
  local src=$1 dst=$2 mode=$3
  if [ -e "$dst" ] && cmp -s "$src" "$dst"; then say "  unchanged  $dst"; return 0; fi
  if [ -e "$dst" ] && [ "$force" != 1 ] && [ "${4:-}" != always ]; then
    say "  SKIPPED    $dst (differs from the devbox's copy; --force replaces it, keeping a .bak)"
    return 0
  fi
  if [ -e "$dst" ] && [ "${4:-}" != always ]; then act cp -p "$dst" "$dst.bak-$STAMP"; fi
  act mkdir -p "$(dirname "$dst")"
  act install -m "$mode" "$src" "$dst.new-$STAMP"
  act mv -f "$dst.new-$STAMP" "$dst"
  if [ "$dry" != 1 ]; then say "  installed  $dst"; fi
}

fetch() { # $1 = path on the devbox (relative to its home), $2 = local file
  scp -q "${SSH_OPTS[@]}" "$host:$1" "$2" || die "cannot fetch $host:$1 (did setup-devbox.sh finish?)"
}

loaded() { launchctl print "gui/$(id -u)/$1" >/dev/null 2>&1; }

if [ "$dry" != 1 ] && [ "$(uname -s)" != Darwin ]; then die "run this on the laptop (macOS), or pass --dry-run"; fi
if [ "$dry" = 1 ]; then say "DRY RUN: nothing below is changed (HOME=$HOME)"; fi

step "the devbox answers over ssh ($host)"
ssh -n "${SSH_OPTS[@]}" "$host" true || die "cannot ssh to $host: AGENT-SETUP.md step 0 (ssh alias, sign-ins)"
say "  ok"

step "binaries: ~/.local/bin/brain (sha256 checked) and ~/.local/bin/devbox"
fetch "$DIST/brain-darwin-arm64" "$tmp/brain"
fetch "$DIST/brain-darwin-arm64.sha256" "$tmp/brain.sha256"
want=$(awk '{print $1}' "$tmp/brain.sha256")
got=$(sha256 "$tmp/brain")
[ "$want" = "$got" ] || die "sha256 mismatch for brain-darwin-arm64: got $got, want $want"
say "  sha256 ok  brain-darwin-arm64"
install_file "$tmp/brain" "$brain" 755 always
fetch "$DIST/devbox" "$tmp/devbox"
install_file "$tmp/devbox" "$HOME/.local/bin/devbox" 755

step "settings: the bridge token and identity.json, from the devbox"
act mkdir -p "$cfg" "$HOME/.local/state/brain"
act chmod 700 "$cfg"
(umask 077 && ssh -n "${SSH_OPTS[@]}" "$host" 'cat .config/brain/bridge.token' >"$tmp/bridge.token") ||
  die "no bridge token on $host (setup-devbox.sh writes it)"
install_file "$tmp/bridge.token" "$cfg/bridge.token" 600 always
(umask 077 && ssh -n "${SSH_OPTS[@]}" "$host" 'cat .config/brain/identity.json' >"$tmp/identity.json") ||
  die "no identity.json on $host (setup-devbox.sh writes it)"
install_file "$tmp/identity.json" "$cfg/identity.json" 600
# fills only what is missing; every value the devbox wrote stays
act "$brain" identity --init </dev/null
act "$brain" identity </dev/null

step "the bridge and its reverse tunnel (LaunchAgents com.saahil.brain-bridge, com.saahil.brain-tunnel)"
if loaded com.saahil.brain-bridge && loaded com.saahil.brain-tunnel && [ "$force" != 1 ]; then
  say "  present    both loaded (--force reinstalls them)"
else
  act "$brain" bridge install </dev/null
fi

step "unison and the hourly brain sync (com.saahil.brain-sync)"
if command -v unison >/dev/null || [ -x /opt/homebrew/bin/unison ]; then say "  present    unison"; else act "$brain" sync --setup </dev/null; fi
if loaded com.saahil.brain-sync && [ "$force" != 1 ]; then say "  present    com.saahil.brain-sync"; else act "$brain" sync --enable </dev/null; fi

step "brain skeleton (~/brain)"
missing=0
for d in work notes hubs memory inbox .brain/evals .import; do [ -d "$HOME/brain/$d" ] || missing=1; done
if [ ! -f "$HOME/brain/.brain/live" ]; then missing=1; fi
if [ "$missing" = 0 ]; then say "  present    $HOME/brain"; else act "$brain" init </dev/null; fi
if [ "$dry" != 1 ]; then say "  live since $(cat "$HOME/brain/.brain/live")"; fi

step "agent hosts on this machine (brain install)"
if [ -x "$brain" ] && "$brain" install --verify </dev/null >/dev/null 2>&1; then
  say "  present    brain install --verify is clean"
else
  act "$brain" install </dev/null
fi
if [ "$dry" = 1 ]; then
  say "  would check: claude plugin list | grep saahil-brain"
elif ! command -v claude >/dev/null; then
  say "  WARN       claude is not on PATH; once it is: claude plugin list | grep saahil-brain"
elif claude plugin list </dev/null 2>/dev/null | grep -q saahil-brain; then
  say "  ok         claude plugin list shows saahil-brain"
else
  die "brain install ran, but claude plugin list has no saahil-brain (brain install --verify says why)"
fi

step "the devbox reaches this laptop through the bridge (brain peer status, from the devbox)"
if [ "$dry" = 1 ]; then
  say "  would: ssh $host '.local/bin/brain peer status' until it answers (up to 60 s)"
  exit 0
fi
for _ in $(seq 1 20); do
  if ssh -n "${SSH_OPTS[@]}" "$host" '.local/bin/brain peer status'; then
    say "BRIDGE OK: the devbox reaches this laptop through the bridge"
    exit 0
  fi
  sleep 3
done
die "the devbox could not reach the bridge yet; see ~/Library/Logs/brain-tunnel.log and brain-bridge.log"
