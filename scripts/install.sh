#!/usr/bin/env bash
# Neo Sell — one-line installer and menu.
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/neoauroraproject/neosell/main/scripts/install.sh)
#
# First run: installs the latest release and asks for a domain (optional),
# SSL and the panel port (a random free port by default).
# Later runs: opens the management menu (update, domain, port, logs, uninstall …).
#
# Non-interactive:   … install.sh install -yes -domain panel.example.com -email you@example.com
# Mirror / fork:     NEOSELL_REPO=owner/name   or   NEOSELL_URL=https://mirror/dir
set -euo pipefail

REPO="${NEOSELL_REPO:-neoauroraproject/neosell}"
BASE="${NEOSELL_URL:-https://github.com/$REPO/releases/latest/download}"
BIN=/usr/local/bin/neosell

coral=$'\033[38;5;209m'; dim=$'\033[2m'; bold=$'\033[1m'; red=$'\033[31m'; off=$'\033[0m'
say()  { printf '  %s\n' "$*"; }
die()  { printf '  %serror:%s %s\n' "$red" "$off" "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root (sudo -i, then the command again)"
[ "$(uname -s)" = Linux ] || die "Linux only"
command -v systemctl >/dev/null || die "systemd is required"
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac
command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; } || die "curl is required"

# `curl … | bash` gives the script a pipe on stdin; answer questions from the terminal.
if [ ! -t 0 ] && [ -r /dev/tty ]; then exec </dev/tty; fi

printf '\n  %s●%s %sNeo Sell%s  %s%s%s\n\n' "$coral" "$off" "$bold" "$off" "$dim" "$REPO" "$off"

fetch() {
  local tmp; tmp="$(mktemp -d)"
  say "downloading neosell-linux-$ARCH …"
  curl -fSL --progress-bar "$BASE/neosell-linux-$ARCH" -o "$tmp/neosell" || die "download failed ($BASE). Is the repository public and does it have a release?"
  if curl -fsSL "$BASE/SHA256SUMS" -o "$tmp/SHA256SUMS" 2>/dev/null; then
    ( cd "$tmp" && grep " neosell-linux-$ARCH\$" SHA256SUMS | sed "s/neosell-linux-$ARCH/neosell/" | sha256sum -c --quiet - ) || die "checksum mismatch — download refused"
    say "checksum verified"
  else
    say "${dim}no SHA256SUMS published; skipped verification${off}"
  fi
  install -m 0755 "$tmp/neosell" "$BIN"
  rm -rf "$tmp"
}

if [ $# -gt 0 ]; then
  # Explicit command, e.g. `install -yes -domain …` or `update`.
  [ -x "$BIN" ] || fetch
  exec "$BIN" "$@"
fi

if [ -x "$BIN" ] && [ -f /etc/neosell/config.json ]; then
  # Already installed: open the menu (it can update, change port/domain, …).
  exec "$BIN" menu
fi

fetch
exec "$BIN" install
