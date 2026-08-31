#!/bin/zsh
D="$(cd "$(dirname "$0")" && pwd)"
osascript -e 'tell application "DaisyDisk" to activate' >/dev/null 2>&1
"$D/height" 456 3 > "$D/fwd.tsv" &
H=$!
"$D/click" 1800 217 700
wait $H
