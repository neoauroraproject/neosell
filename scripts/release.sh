#!/usr/bin/env bash
# Builds and publishes a GitHub release that `neosell update`, the panel's
# update page and install.sh consume.
#   scripts/release.sh v0.2.0 "notes…"
set -euo pipefail
cd "$(dirname "$0")/.."
TAG="${1:?usage: release.sh vX.Y.Z [notes]}"
NOTES="${2:-Neo Sell $TAG}"
VERSION="${TAG#v}" ./scripts/build.sh
gh release create "$TAG" dist/neosell-linux-amd64 dist/neosell-linux-arm64 dist/install.sh dist/SHA256SUMS \
  --title "Neo Sell $TAG" --notes "$NOTES"
