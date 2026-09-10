#!/usr/bin/env bash
# Regenerate the README screenshots. Renders the dashboard with the frame
# command (synthetic data) and converts the ANSI capture to PNG.
set -euo pipefail

cd "$(dirname "$0")/.."
BIN=${BIN:-./tui-chart}
COLS=${COLS:-150}
ROWS=${ROWS:-42}
TMP=${TMP:-.preview}

if [ ! -x "$BIN" ]; then
  go build -o "$BIN" ./cmd/tui-chart
fi
mkdir -p "$TMP" docs

shot() {
  local name=$1
  shift
  "$BIN" frame --demo --color truecolor --ticks 220 --width "$COLS" --height "$ROWS" "$@" \
    >"$TMP/$name.ans"
  python3 tools/ansipng/ansipng.py "$TMP/$name.ans" "docs/$name.png" -w "$COLS" -r "$ROWS" >/dev/null
  echo "docs/$name.png"
}

shot screenshot-cpu
shot screenshot-breakdown   --source cpu --keys d
shot screenshot-memory      --source memory --keys d
shot screenshot-network     --source network --keys d
shot screenshot-bars        --keys v,v
shot screenshot-nord        --theme nord --keys g
shot screenshot-light       --theme light --source temperature
shot screenshot-help        --keys '?'
