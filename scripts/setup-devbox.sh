#!/bin/bash
# setup-devbox.sh — set up the brain on the devbox (SETUP.md step 1). Run it ON the devbox, from the unzipped release:
#
#   scripts/setup-devbox.sh --laptop-home /Users/<user> [--profile NAME] [--devbox-host H] [--orca-env E]
#                           [--work-repo P] [--brain-repo P] [--aws-profile P] [--dry-run] [--force]
#
# --profile NAME merges the team settings in profiles/NAME.json under your own (brain identity --init --profile).
# Each step prints what it did, or that it was already done, so a re-run is safe. --dry-run prints every change and
# makes none (on any OS). --force replaces a file that differs from the kit (a .bak is kept first) and passes --force
# to `brain identity --init`. sudo is used only for the bind mount and for linger; each step says why first.
# --laptop-home is required until identity.json holds laptop_home: the devbox cannot guess the laptop's login.
set -euo pipefail

TEMPORAL_VERSION=1.7.0
TEMPORAL_SHA256=8b5de72e622f4ae062d0d5d948ca398de6212d63b2f25766a1cb810a3dc2d0ed # temporal_cli_1.7.0_linux_amd64.tar.gz

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() { printf 'setup-devbox: %s\n' "$*" >&2; exit 1; }

dry=0 force=0 laptop_home="" devbox_host="" orca_env="" work_repo="" brain_repo="" aws_profile="" profile=""
while [ $# -gt 0 ]; do
  case $1 in
    --dry-run) dry=1 ;;
    --force) force=1 ;;
    --laptop-home | --devbox-host | --orca-env | --work-repo | --brain-repo | --aws-profile | --profile)
      [ $# -ge 2 ] || die "$1 needs a value"
      name=${1#--}
      printf -v "${name//-/_}" '%s' "$2"
      shift ;;
    -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d'; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
  shift
done

act() { if [ "$dry" = 1 ]; then say "  would: $*"; else "$@"; fi; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }

KIT=$(cd "$(dirname "$0")/.." && pwd)
IDENTITY=$HOME/.config/brain/identity.json
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

ident() { # $1 = key: its string value in identity.json, or nothing
  [ -f "$IDENTITY" ] || return 0
  python3 -c 'import json,sys; v=json.load(open(sys.argv[1])).get(sys.argv[2]); print(v if isinstance(v,str) else "")' "$IDENTITY" "$1"
}

# Put $1 at $2 with mode $3. Same bytes: left alone. Different: replaced only with --force, after a .bak copy.
# Sets changed=1 when $2 is (or, in a dry run, would be) written.
install_file() {
  local src=$1 dst=$2 mode=$3
  changed=0
  if [ -e "$dst" ] && cmp -s "$src" "$dst"; then say "  unchanged  $dst"; return 0; fi
  if [ -e "$dst" ] && [ "$force" != 1 ]; then say "  SKIPPED    $dst (differs from the kit; --force replaces it, keeping a .bak)"; return 0; fi
  if [ -e "$dst" ]; then act cp -p "$dst" "$dst.bak-$STAMP"; fi
  act mkdir -p "$(dirname "$dst")"
  # a new inode, so a running copy of a binary keeps its own file
  act install -m "$mode" "$src" "$dst.new-$STAMP"
  act mv -f "$dst.new-$STAMP" "$dst"
  if [ "$dry" != 1 ]; then say "  installed  $dst"; fi
  changed=1
}

check_sum() { # $1 = a path inside the kit, checked against SHA256SUMS
  local want got
  want=$(awk -v f="$1" '$2 == f || $2 == "*"f {print $1}' "$KIT/SHA256SUMS")
  got=$(sha256 "$KIT/$1")
  if [ -z "$want" ] || [ "$want" != "$got" ]; then die "sha256 mismatch for $1: got $got, SHA256SUMS says ${want:-nothing}"; fi
  say "  sha256 ok  $1"
}

# Units: start a stopped one; restart a running one only when its file or binary changed in this run (never orca-serve,
# whose restart would end every agent terminal on the box).
enable_unit() { # $1 = unit, $2 = 1 when it changed, $3 = restart|never
  if systemctl --user is-enabled -q "$1" 2>/dev/null && systemctl --user is-active -q "$1" 2>/dev/null; then
    if [ "$2" = 1 ] && [ "$3" = restart ]; then
      act systemctl --user restart "$1"
    elif [ "$2" = 1 ]; then
      say "  changed    $1: restart it yourself when no agent runs (systemctl --user restart $1)"
    else
      say "  running    $1"
    fi
  else
    act systemctl --user enable --now "$1"
  fi
}

if [ "$dry" != 1 ] && [ "$(uname -s)" != Linux ]; then die "run this ON the devbox (Linux), or pass --dry-run"; fi
if [ "$dry" = 1 ]; then say "DRY RUN: nothing below is changed (HOME=$HOME)"; fi
for f in SHA256SUMS bin/brain-linux-amd64 bin/brain-worker-linux-amd64 bin/brain-darwin-arm64 scripts/setup-laptop.sh \
  scripts/devbox scripts/brain-care deploy/systemd/brain-temporal.service; do
  [ -e "$KIT/$f" ] || die "$KIT/$f is missing: run this from an unzipped release (make dist builds one in dist/)"
done

user=$(id -un)
devbox_home=$HOME
laptop_home=${laptop_home:-$(ident laptop_home)}
[ -n "$laptop_home" ] || die "pass --laptop-home /Users/<your laptop login> (the bind mount needs it)"
brain_repo=${brain_repo:-$(ident brain_repo)}
brain_repo=${brain_repo:-$laptop_home/Documents/saahil-brain}
devbox_host=${devbox_host:-$(ident devbox_host)}
devbox_host=${devbox_host:-$(basename "$laptop_home")-devbox}
brain=$HOME/.local/bin/brain
temporal=$HOME/.local/bin/temporal

step "bind mount: $devbox_home at $laptop_home (every brain path is the same string on both machines)"
fstab_line="$devbox_home $laptop_home none bind,nofail 0 0"
if [ "$laptop_home" = "$devbox_home" ]; then
  say "  not needed (both homes are the same path)"
else
  if grep -qE "^[^#]*[[:space:]]${laptop_home}[[:space:]]+none[[:space:]]+bind" /etc/fstab 2>/dev/null; then
    say "  present    in /etc/fstab"
  else
    say "  sudo: adds one line to /etc/fstab (backed up first) so the laptop's paths resolve here: $fstab_line"
    act sudo cp -p /etc/fstab "/etc/fstab.bak-$STAMP"
    act sudo mkdir -p "$laptop_home"
    if [ "$dry" = 1 ]; then
      say "  would: append to /etc/fstab: $fstab_line"
    else
      printf '%s\n' "$fstab_line" | sudo tee -a /etc/fstab >/dev/null
      say "  added      $fstab_line"
    fi
  fi
  if [ -d "$laptop_home" ] && [ "$(stat -c %d:%i "$laptop_home" 2>/dev/null)" = "$(stat -c %d:%i "$devbox_home" 2>/dev/null)" ]; then
    say "  mounted    $laptop_home is $devbox_home"
  else
    say "  sudo: mounts it now, without a reboot"
    act sudo mount "$laptop_home"
  fi
fi

step "binaries into ~/.local/bin (checked against SHA256SUMS)"
check_sum bin/brain-linux-amd64
install_file "$KIT/bin/brain-linux-amd64" "$brain" 755
brain_changed=$changed
check_sum bin/brain-worker-linux-amd64
install_file "$KIT/bin/brain-worker-linux-amd64" "$HOME/.local/bin/brain-worker" 755
worker_changed=$changed

step "settings: $IDENTITY"
init_args=(identity --init --laptop-home "$laptop_home" --devbox-home "$devbox_home" --brain-repo "$brain_repo"
  --devbox-host "$devbox_host")
if [ -n "$profile" ]; then init_args+=(--profile "$profile"); fi
if [ -n "$orca_env" ]; then init_args+=(--orca-env "$orca_env"); fi
if [ -n "$work_repo" ]; then init_args+=(--work-repo "$work_repo"); fi
if [ "$force" = 1 ]; then init_args+=(--force); fi
act "$brain" "${init_args[@]}"
# scripts/devbox on the laptop starts and stops this instance; the instance metadata service knows its id and region
if [ "$dry" = 1 ]; then
  say "  would: add devbox_instance_id and devbox_region (instance metadata) and devbox_aws_profile where missing"
else
  imds_token=$(curl -fsS -m 2 -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null || true)
  imds() { if [ -n "$imds_token" ]; then curl -fsS -m 2 -H "X-aws-ec2-metadata-token: $imds_token" "http://169.254.169.254/latest/meta-data/$1" 2>/dev/null || true; fi; }
  python3 - "$IDENTITY" "$force" "devbox_instance_id=$(imds instance-id)" "devbox_region=$(imds placement/region)" \
    "devbox_aws_profile=$aws_profile" <<'PY'
import json, os, sys
path, force, pairs = sys.argv[1], sys.argv[2] == "1", sys.argv[3:]
data = json.load(open(path)) if os.path.exists(path) else {}
for key, value in (p.split("=", 1) for p in pairs):
    if not value:
        print(f"  unknown    {key} (off EC2 or not given; add it by hand if the laptop needs it)")
    elif data.get(key) == value or (key in data and not force):
        print(f"  present    {key}")
    else:
        data[key] = value
        print(f"  set        {key}={value}")
tmp = path + ".new"
with open(os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
os.replace(tmp, path)
PY
fi

step "brain skeleton ($laptop_home/brain)"
missing=0
for d in work notes hubs memory inbox .brain/evals .import; do [ -d "$laptop_home/brain/$d" ] || missing=1; done
if [ ! -f "$laptop_home/brain/.brain/live" ]; then missing=1; fi
if [ "$missing" = 0 ]; then say "  present    $laptop_home/brain"; else act "$brain" init; fi
if [ "$dry" != 1 ]; then say "  live since $(cat "$laptop_home/brain/.brain/live")"; fi

step "Temporal CLI $TEMPORAL_VERSION or newer ($temporal)"
have=""
if [ -x "$temporal" ]; then have=$("$temporal" --version 2>/dev/null | awk '{print $3}'); fi
if [ -n "$have" ] && [ "$(printf '%s\n' "$TEMPORAL_VERSION" "$have" | sort -V | head -1)" = "$TEMPORAL_VERSION" ]; then
  say "  present    temporal $have"
else
  url=https://github.com/temporalio/cli/releases/download/v$TEMPORAL_VERSION/temporal_cli_${TEMPORAL_VERSION}_linux_amd64.tar.gz
  if [ "$dry" = 1 ]; then
    say "  would: download $url, check its sha256 is $TEMPORAL_SHA256, install temporal"
  else
    curl -fsSL --retry 3 -m 600 -o "$tmp/temporal.tar.gz" "$url"
    got=$(sha256 "$tmp/temporal.tar.gz")
    [ "$got" = "$TEMPORAL_SHA256" ] || die "temporal download: sha256 $got, expected $TEMPORAL_SHA256"
    tar -xzf "$tmp/temporal.tar.gz" -C "$tmp" temporal
    install -m 755 "$tmp/temporal" "$temporal.new-$STAMP"
    mv -f "$temporal.new-$STAMP" "$temporal"
    say "  installed  temporal $TEMPORAL_VERSION (sha256 checked)"
  fi
fi

step "systemd --user units (rendered from deploy/systemd: @LAPTOP_HOME@ @DEVBOX_HOME@ @USER@)"
units=$HOME/.config/systemd/user
any_changed=0 temporal_changed=0 orca_changed=0 care_changed=0
for tpl in "$KIT"/deploy/systemd/*.service "$KIT"/deploy/systemd/*.timer; do
  unit=${tpl##*/}
  sed -e "s|@LAPTOP_HOME@|$laptop_home|g" -e "s|@DEVBOX_HOME@|$devbox_home|g" -e "s|@USER@|$user|g" "$tpl" >"$tmp/$unit"
  install_file "$tmp/$unit" "$units/$unit" 644
  if [ "$changed" = 1 ]; then any_changed=1; fi
  case $unit in
    brain-temporal.service) temporal_changed=$changed ;;
    brain-worker.service) if [ "$changed" = 1 ]; then worker_changed=1; fi ;;
    orca-serve.service) orca_changed=$changed ;;
    brain-care.timer) care_changed=$changed ;;
  esac
done
if [ "$(loginctl show-user "$user" -p Linger 2>/dev/null)" = Linger=yes ]; then
  say "  present    linger"
else
  say "  sudo: linger keeps these services running while nobody is logged in"
  act sudo loginctl enable-linger "$user"
fi
if [ "$any_changed" = 1 ]; then act systemctl --user daemon-reload; fi
enable_unit brain-temporal.service "$temporal_changed" restart
# Temporal resumes a running workflow after a worker restart
enable_unit brain-worker.service "$worker_changed" restart
if [ -x /opt/orca/orca-linux.AppImage ]; then
  enable_unit orca-serve.service "$orca_changed" never
else
  say "  todo       Orca is not at /opt/orca/orca-linux.AppImage (SETUP.md step 0); orca-serve stays off"
fi

step "dream schedule and 30-day retention (namespace brain on 127.0.0.1:7233)"
temporal_up() { [ -x "$temporal" ] && "$temporal" operator namespace describe -n brain --address 127.0.0.1:7233 >/dev/null 2>&1; }
if [ "$dry" != 1 ]; then
  for _ in $(seq 1 24); do if temporal_up; then break; fi; sleep 5; done
  temporal_up || die "Temporal did not answer within 2 min (journalctl --user -u brain-temporal)"
fi
if temporal_up; then
  ttl=$("$temporal" operator namespace describe -n brain --address 127.0.0.1:7233 -o json |
    python3 -c 'import json,sys; print(json.load(sys.stdin).get("config",{}).get("workflowExecutionRetentionTtl",""))')
else
  ttl=""
fi
act "$brain" dream schedule
if [ "$ttl" = 2592000s ]; then
  say "  present    retention 720h"
else
  act "$temporal" operator namespace update -n brain --address 127.0.0.1:7233 --retention 720h
fi

step "bridge token (~/.config/brain/bridge.token; setup-laptop.sh copies it)"
token=$HOME/.config/brain/bridge.token
if [ -f "$token" ] && [ "$(tr -d '[:space:]' <"$token" | wc -c)" -ge 32 ]; then
  say "  present    $token"
elif [ "$dry" = 1 ]; then
  say "  would: write 64 random hex characters to $token (mode 600)"
else
  mkdir -p "$(dirname "$token")"
  chmod 700 "$(dirname "$token")"
  (umask 077 && od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$token")
  say "  created    $token"
fi

step "agent hosts on this machine (brain install)"
# --dry-run keeps the check read-only (a bare install --verify writes BRAIN.md and the mod first); a new binary
# always installs, since the read-only check cannot see its newer mod
if [ "$brain_changed" = 0 ] && [ -x "$brain" ] && "$brain" install --dry-run --verify >/dev/null 2>&1; then
  say "  present    brain install --dry-run --verify is clean"
else
  act "$brain" install
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

step "laptop files staged in ~/.local/share/brain/dist (setup-laptop.sh fetches them over ssh)"
dist=$HOME/.local/share/brain/dist
check_sum bin/brain-darwin-arm64
install_file "$KIT/bin/brain-darwin-arm64" "$dist/brain-darwin-arm64" 755
# the same "<sum>  <name>" file `brain self-update` reads
printf '%s  brain-darwin-arm64\n' "$(sha256 "$KIT/bin/brain-darwin-arm64")" >"$tmp/brain-darwin-arm64.sha256"
install_file "$tmp/brain-darwin-arm64.sha256" "$dist/brain-darwin-arm64.sha256" 644
install_file "$KIT/scripts/setup-laptop.sh" "$dist/setup-laptop.sh" 755
install_file "$KIT/scripts/devbox" "$dist/devbox" 755

step "daily care (SETUP.md step 4): brain-care.timer"
if [ -e "$brain_repo/.git" ]; then
  install_file "$KIT/scripts/brain-care" "$HOME/.local/bin/brain-care" 755
  # the care agent pushes to the brain repo with the devbox's gh sign-in
  if [ -n "$(git -C "$brain_repo" config --local --get credential.helper || true)" ]; then
    say "  present    credential.helper in $brain_repo"
  else
    act git -C "$brain_repo" config --local credential.helper '!gh auth git-credential'
  fi
  enable_unit brain-care.timer "$care_changed" restart
  say "  first run, when you want one: systemctl --user start brain-care.service"
else
  say "  todo       clone your brain code into $brain_repo (step 4), then re-run this script"
fi

step "next, on the laptop"
say "  scp $devbox_host:.local/share/brain/dist/setup-laptop.sh /tmp/ && bash /tmp/setup-laptop.sh $devbox_host"
