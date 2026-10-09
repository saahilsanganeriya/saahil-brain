#!/bin/bash
# The one-line laptop setup, piped from the devbox (the host defaults to devbox_host in identity.json):
#   ssh <devbox-host> cat .local/share/brain/dist/bootstrap-laptop.sh | bash -s -- <devbox-host>
# It fetches setup-laptop.sh from the same folder and runs it with stdin closed, so nothing it runs reads this pipe.
# Later binaries arrive with `brain self-update` (the devbox asks for it through the bridge).
set -euo pipefail
host=${1:-}
identity=$HOME/.config/brain/identity.json
if [ -z "$host" ] && [ -f "$identity" ]; then host=$(plutil -extract devbox_host raw -o - "$identity" 2>/dev/null || true); fi
host=${host:-$(id -un)-devbox}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
scp -q -o BatchMode=yes -o ControlPath=none -o ConnectTimeout=10 "$host:.local/share/brain/dist/setup-laptop.sh" "$tmp/setup-laptop.sh"
bash "$tmp/setup-laptop.sh" "$host" </dev/null
