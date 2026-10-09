#!/bin/bash
# build-kit.sh <version> — `make dist`: dist/brain-setup-<version>.zip, a release of this repository: the committed
# tree (git archive HEAD, so the root README.md, SETUP.md and docs/ come with it), plus bin/ from `make build` and a
# SHA256SUMS over every file. RELEASE=1 refuses a dirty tree or a missing README.md / SETUP.md, then runs check-kit.sh.
set -euo pipefail
version=${1:?usage: build-kit.sh <version>}
release=${RELEASE:-0}
cd "$(dirname "$0")/.."

if [ "$release" = 1 ] && [ "${version%-dirty}" != "$version" ]; then
  echo "build-kit: a release needs a clean tree: bin/ is built from the working tree, the rest from HEAD" >&2
  exit 1
fi
name=brain-setup-$version
out=dist/$name
rm -rf "$out" "$out.zip"
mkdir -p dist
git archive --format=tar --prefix="$name/" HEAD | tar -x -C dist
for doc in README.md SETUP.md; do
  if [ -f "$out/$doc" ]; then
    :
  elif [ "$release" = 1 ]; then
    echo "build-kit: $doc is not committed at the repo root; a release needs it" >&2
    exit 1
  else
    echo "build-kit: WARNING $doc is not committed at the repo root (fine for a draft; RELEASE=1 refuses)" >&2
  fi
done
mkdir -p "$out/bin"
install -m 755 bin/brain-darwin-arm64 bin/brain-linux-amd64 bin/brain-worker-linux-amd64 "$out/bin/"

(cd "$out" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum) >"$out.sums"
mv "$out.sums" "$out/SHA256SUMS"
# -X drops uid/gid extras; the zip holds the one top folder
(cd dist && zip -qrX "$name.zip" "$name")
echo "built dist/$name.zip ($(du -h "dist/$name.zip" | cut -f1), $(wc -l <"$out/SHA256SUMS") files)"
if [ "$release" = 1 ]; then scripts/check-kit.sh "dist/$name.zip"; fi
