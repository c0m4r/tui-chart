#!/usr/bin/env bash
# End to end test: run the real binary under a pseudo terminal, drive it with
# keys, and check what the terminal would show. Requires python3; uses the
# capture and preview tools from tools/.
set -euo pipefail

cd "$(dirname "$0")/.."
BIN=${BIN:-./tui-chart}
COLS=${COLS:-120}
ROWS=${ROWS:-34}
OUT=${OUT:-.preview/uitest}
mkdir -p "$OUT"

if [ ! -x "$BIN" ]; then
  echo "building $BIN"
  go build -o "$BIN" ./cmd/tui-chart
fi

pass=0
fail=0

# screen <name> <espera> <keys> [extra args...] -> prints the final screen text
screen() {
  local name=$1 settle=$2 keys=$3
  shift 3
  python3 tools/ptytest/capture.py \
    --cols "$COLS" --rows "$ROWS" --settle "$settle" --keys "$keys" \
    --out "$OUT/$name.bin" -- "$BIN" "$@" >/dev/null 2>&1
  python3 tools/ansipng/ansipng.py "$OUT/$name.bin" "$OUT/$name.png" \
    -w "$COLS" -r "$ROWS" --dump-text 2>/dev/null
}

# expect <name> <needle> <keys> [extra args...]
expect() {
  local name=$1 needle=$2 keys=$3
  shift 3
  local text
  if ! text=$(screen "$name" 1.0 "$keys" --demo "$@"); then
    echo "FAIL  $name: capture failed"
    fail=$((fail + 1))
    return
  fi
  if grep -qF -- "$needle" <<<"$text"; then
    echo "PASS  $name  ($needle)"
    pass=$((pass + 1))
  else
    echo "FAIL  $name  expected to find '$needle'"
    fail=$((fail + 1))
  fi
}

# The dashboard opens on CPU and the arrow keys move between sources.
expect start          "CPU · total"        "0.1:noop"
expect right-memory   "Memory · total"     "1.0:right"
expect right-swap     "Swap"               "1.0:right,0.5:right"
expect left-wraps     "Temp"               "1.0:left"
expect digit-jump     "Network"            "1.0:5"
expect tab-next       "Memory"             "1.0:tab"
# Breakdown views.
expect cpu-breakdown  "CPU · breakdown"    "1.0:d"
expect mem-breakdown  "Memory · breakdown" "1.0:right,0.5:d"
# Overlays and state.
expect help-overlay   "NAVIGATION"         "1.0:?"
expect help-closes    "CPU · total"        "1.0:?,0.5:esc"
expect stats-table    "statistics"         "1.0:s"
expect paused         "PAUSED"             "1.0:space"
expect resumed        "CPU · total"        "1.0:space,0.5:space"
expect cleared        "history cleared"    "1.0:r"
# Renderer and window controls.
expect line-view      "view: line"         "1.0:v"
expect bars-view      "view: bars"         "1.0:v,0.4:v"
expect zoom           "window 2m00s"       "1.0:+"
expect theme-cycle    "theme: mono"        "1.0:t"
# Sizing: the sidebar disappears on a narrow terminal but the chart remains.
COLS=70 ROWS=22 expect narrow-chart "CPU · total" "0.1:noop"
COLS=70 ROWS=22 expect narrow-keys  "Memory"      "1.0:right"
# Below the minimum size the dashboard explains itself instead of drawing a
# chart that would be clipped.
COLS=50 ROWS=14 expect tiny-notice  "terminal too small" "0.1:noop"
COLS=50 ROWS=14 expect tiny-keys    "terminal too small" "1.0:right"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
