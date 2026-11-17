#!/usr/bin/env bash
# Builds static release binaries (no cgo) for linux/amd64 and linux/arm64 into dist/,
# plus SHA256SUMS. The web UI is embedded from internal/webui/dist.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.1.0)}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X neosell/internal/version.Version=$VERSION -X neosell/internal/version.Commit=$COMMIT -X neosell/internal/version.Date=$DATE"

go vet ./...
go test ./...
mkdir -p dist
for arch in amd64 arm64; do
  echo "building linux/$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "dist/neosell-linux-$arch" ./cmd/neosell
done
cp scripts/install.sh dist/install.sh
(cd dist && sha256sum neosell-linux-amd64 neosell-linux-arm64 install.sh | sed 's/ \*/  /' > SHA256SUMS)
ls -lh dist
