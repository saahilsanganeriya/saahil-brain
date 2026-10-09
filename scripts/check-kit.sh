#!/bin/bash
# check-kit.sh <brain-setup-VERSION.zip> — the release gate for a setup kit: the layout, the checksums, and nothing
# of the owner's anywhere in it, source/ unpacked too (paths, host, Orca ids, keys, AWS keys, this machine's bridge
# token, brain data). Exit 0 when clean; exit 1 listing every problem by file, never the matched text.
set -euo pipefail
zip=${1:?usage: check-kit.sh <brain-setup-VERSION.zip>}
[ -f "$zip" ] || { echo "check-kit: no such file: $zip" >&2; exit 2; }
export LC_ALL=C
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
problems=0
bad() { printf 'FAIL  %s\n' "$*"; problems=$((problems + 1)); }
ok() { printf 'ok    %s\n' "$*"; }

unzip -q "$zip" -d "$tmp/zip"
tops=("$tmp"/zip/*)
if [ ${#tops[@]} != 1 ] || [ ! -d "${tops[0]}" ]; then echo "FAIL  the zip must hold exactly one folder, brain-setup-<version>/"; exit 1; fi
kit=${tops[0]}
case ${kit##*/} in brain-setup-*) ok "one top folder ${kit##*/}/" ;; *) bad "top folder ${kit##*/} is not brain-setup-<version>" ;; esac

# ---- layout
before=$problems
for f in README.md AGENT-SETUP.md SHA256SUMS identity.example.json \
  docs/ARCHITECTURE.md docs/DEVBOX-AND-ORCA.md docs/BACKFILL.md docs/OPERATIONS.md docs/LESSONS.md docs/CONFIG.md \
  bin/brain-darwin-arm64 bin/brain-linux-amd64 bin/brain-worker-linux-amd64 \
  scripts/setup-devbox.sh scripts/setup-laptop.sh scripts/devbox scripts/brain-care \
  deploy/systemd/brain-temporal.service deploy/systemd/brain-worker.service deploy/systemd/brain-care.service \
  deploy/systemd/brain-care.timer deploy/systemd/orca-serve.service \
  templates/brain-care-goal.md templates/brain-care-playbook.md; do
  [ -f "$kit/$f" ] || bad "missing $f"
done
for f in "$kit"/bin/* "$kit"/scripts/*; do
  if [ -f "$f" ] && [ ! -x "$f" ]; then bad "not executable: ${f#"$kit"/}"; fi
done
sources=("$kit"/source/saahil-brain-*.tar.gz)
if [ ${#sources[@]} = 1 ] && [ -f "${sources[0]}" ]; then
  mkdir "$tmp/src"
  tar -xzf "${sources[0]}" -C "$tmp/src"
else
  bad "source/ must hold one saahil-brain-<version>.tar.gz"
fi
for d in notes hubs memory sessions journal inbox work .brain .index .data .archive .import; do
  if [ -e "$kit/$d" ]; then bad "brain data folder at the top: $d/"; fi
done
while IFS= read -r f; do bad "file that must not ship: ${f#"$tmp"/}"; done < <(find "$tmp/zip" "$tmp/src" -type f \( -name '*.db' -o \
  -name '*.jsonl' -o -name bridge.token -o -name identity.json -o -name 'id_ed25519*' -o -name 'id_rsa*' -o -name '*.pem' -o -name .env \) 2>/dev/null)
if [ "$problems" = "$before" ]; then ok "layout"; fi

# ---- checksums: every file listed, every sum right
before=$problems
if (cd "$kit" && if command -v sha256sum >/dev/null; then sha256sum -c SHA256SUMS; else shasum -a 256 -c SHA256SUMS; fi) >"$tmp/sums" 2>&1; then
  :
else
  while IFS= read -r line; do bad "checksum: $line"; done < <(grep -E ': FAILED( open or read)?$' "$tmp/sums" | head -20)
fi
(cd "$kit" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | sort) >"$tmp/files"
awk '{sub(/^\*/, "", $2); print $2}' "$kit/SHA256SUMS" | sort >"$tmp/listed"
while IFS= read -r f; do bad "not in SHA256SUMS: $f"; done < <(comm -23 "$tmp/files" "$tmp/listed")
if [ "$problems" = "$before" ]; then ok "checksums ($(wc -l <"$tmp/listed" | tr -d ' ') files)"; fi

# ---- nothing of the owner's. Each pattern is spelled so it does not match this file, which ships in source/ too.
before=$problems
patterns=(
  'owner laptop home|/Users/saahilsanganeriy[a]'
  'owner devbox home|/home/saahi[l]'
  'owner devbox host|saahil-devbo[x]'
  'owner surname|[Ss][Aa][Nn][Gg][Aa][Nn][Ee][Rr][Ii][Yy][Aa]'
  'owner first name|(^|[^A-Za-z])Saahi[l]([^A-Za-z]|$)'
  'owner Orca environment id|3bf7fe6[6]'
  'owner Orca repo id|2048d3a[a]'
  'ssh private key|BEGIN OPENSS[H]'
  'private key|BEGIN ([A-Z]+ )?PRIVATE KE[Y]'
  'AWS access key id|(AKIA|ASIA)[0-9A-Z]{16}'
  'AWS secret key|(aws_secret_access_key|AWS_SECRET_ACCESS_KEY)["'"'"':=[:space:]]+[A-Za-z0-9/+]{40}'
)
# the session redaction tests need key-shaped synthetic secrets; only these files may hold them, only those patterns
fixture() { # $1 = label, $2 = file
  case $1 in *"private key" | "AWS access key id") ;; *) return 1 ;; esac
  case $2 in "$tmp"/src/*/internal/session/session_test.go | "$tmp"/src/*/testdata/session/main.ndjson) return 0 ;; esac
  return 1
}
scan=("$kit")
if [ -d "$tmp/src" ]; then scan+=("$tmp/src"); fi
for entry in "${patterns[@]}"; do
  label=${entry%%|*}
  while IFS= read -r f; do
    if fixture "$label" "$f"; then
      printf 'note  synthetic %s in %s (redaction test fixture, allowed)\n' "$label" "${f#"$tmp"/}"
    else
      bad "$label in ${f#"$tmp"/}"
    fi
  done < <(grep -rlE -e "${entry#*|}" "${scan[@]}" || true)
done
token=$HOME/.config/brain/bridge.token
if [ -r "$token" ]; then
  tr -d '[:space:]' <"$token" >"$tmp/token"
  if [ -s "$tmp/token" ]; then
    while IFS= read -r f; do bad "this machine's bridge token in ${f#"$tmp"/}"; done < <(grep -rlF -f "$tmp/token" "${scan[@]}" || true)
  fi
fi
if [ "$problems" = "$before" ]; then ok "no owner paths, hosts, names, Orca ids, keys or tokens (source/ included)"; fi

if [ "$problems" = 0 ]; then
  echo "PASS  $zip"
else
  echo "FAIL  $zip: $problems problems"
  exit 1
fi
