#!/bin/zsh
D="$(cd "$(dirname "$0")" && pwd)"
osascript -e 'tell application "DaisyDisk" to activate' >/dev/null 2>&1
"$D/height" 456 3 > "$D/back.tsv" &
H=$!
"$D/click" 987 167 700
wait $H
