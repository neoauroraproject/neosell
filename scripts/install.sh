#!/usr/bin/env bash
# Neo Sell panel installer.
#   bash <(curl -fsSL <release-url>/install.sh) [--port 2087] [--public-url https://panel.example.com] [--tls self|off|file]
# Set NEOSELL_URL to the directory that holds neosell-linux-{amd64,arm64} and SHA256SUMS.
set -euo pipefail

BASE="${NEOSELL_URL:-https://github.com/neoauroraproject/neosell/releases/latest/download}"
[ "$(id -u)" = 0 ] || { echo "Run as root (sudo)."; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "Unsupported CPU: $(uname -m)"; exit 1 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
echo "Downloading neosell-linux-$ARCH …"
curl -fsSL "$BASE/neosell-linux-$ARCH" -o "$TMP/neosell"
if curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS" 2>/dev/null; then
  (cd "$TMP" && grep " neosell-linux-$ARCH\$" SHA256SUMS | sed "s/neosell-linux-$ARCH/neosell/" | sha256sum -c -) || { echo "Checksum mismatch"; exit 1; }
fi
install -m 0755 "$TMP/neosell" /usr/local/bin/neosell
/usr/local/bin/neosell install "$@"
