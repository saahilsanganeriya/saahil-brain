#!/bin/bash
# publish.sh <dist> — install bin/brain-linux-amd64 as the devbox's brain and stage the darwin build + bootstrap.
set -euo pipefail
dist=$1
mkdir -p "$dist" "$HOME/.local/bin"
install -m 755 bin/brain-linux-amd64 "$HOME/.local/bin/.brain.new" && mv -f "$HOME/.local/bin/.brain.new" "$HOME/.local/bin/brain"
if [ -f bin/brain-worker-linux-amd64 ]; then
  install -m 755 bin/brain-worker-linux-amd64 "$HOME/.local/bin/.brain-worker.new" && mv -f "$HOME/.local/bin/.brain-worker.new" "$HOME/.local/bin/brain-worker"
  systemctl --user try-restart brain-worker.service 2>/dev/null || true   # Temporal resumes any running workflow
fi
install -m 755 bin/brain-darwin-arm64 "$dist/.brain-darwin-arm64.new"
(cd "$dist" && mv -f .brain-darwin-arm64.new brain-darwin-arm64 && sha256sum brain-darwin-arm64 > brain-darwin-arm64.sha256)
install -m 644 scripts/bootstrap-laptop.sh "$dist/bootstrap-laptop.sh"
echo "published $(brain version) and darwin $(cut -c1-12 "$dist/brain-darwin-arm64.sha256")"
