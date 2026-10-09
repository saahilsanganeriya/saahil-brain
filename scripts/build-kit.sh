#!/bin/bash
# build-kit.sh <version> — `make dist`: assemble dist/brain-setup-<version>.zip from bin/ (make build), the setup
# scripts, deploy/systemd templates, kit/*.md and a git archive of HEAD (code only). RELEASE=1 fails when README.md or
# AGENT-SETUP.md is missing under kit/ and then runs check-kit.sh; otherwise a missing doc is a warning.
set -euo pipefail
version=${1:?usage: build-kit.sh <version>}
release=${RELEASE:-0}
cd "$(dirname "$0")/.."

name=brain-setup-$version
out=dist/$name
rm -rf "$out" "$out.zip"
mkdir -p "$out/bin" "$out/scripts" "$out/deploy/systemd" "$out/templates" "$out/source" "$out/docs"

install -m 755 bin/brain-darwin-arm64 bin/brain-linux-amd64 bin/brain-worker-linux-amd64 "$out/bin/"
install -m 755 scripts/setup-devbox.sh scripts/setup-laptop.sh scripts/devbox scripts/brain-care "$out/scripts/"
install -m 644 deploy/systemd/*.service deploy/systemd/*.timer "$out/deploy/systemd/"
install -m 644 templates/brain-care-goal.md templates/brain-care-playbook.md "$out/templates/"
install -m 644 identity.example.json "$out/"
git archive --format=tar.gz --prefix="saahil-brain-$version/" -o "$out/source/saahil-brain-$version.tar.gz" HEAD

for doc in README.md AGENT-SETUP.md; do
  if [ -f "kit/$doc" ]; then
    install -m 644 "kit/$doc" "$out/"
  elif [ "$release" = 1 ]; then
    echo "build-kit: kit/$doc is missing; a release needs it" >&2
    exit 1
  else
    echo "build-kit: WARNING kit/$doc is missing (fine for a draft; RELEASE=1 refuses)" >&2
  fi
done
for doc in kit/docs/*.md; do
  if [ -f "$doc" ]; then install -m 644 "$doc" "$out/docs/"; fi
done

(cd "$out" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum) >"$out.sums"
mv "$out.sums" "$out/SHA256SUMS"
# -X drops uid/gid extras; the zip holds the one top folder
(cd dist && zip -qrX "$name.zip" "$name")
echo "built dist/$name.zip ($(du -h "dist/$name.zip" | cut -f1), $(wc -l <"$out/SHA256SUMS") files)"
if [ "$release" = 1 ]; then scripts/check-kit.sh "dist/$name.zip"; fi
