#!/bin/zsh
D="$(cd "$(dirname "$0")" && pwd)"
osascript -e 'tell application "DaisyDisk" to activate' >/dev/null 2>&1
"$D/click" 1400 500 200 >/dev/null    # neutral click inside the window to focus it
rm -rf "$D/back"; mkdir -p "$D/back"
"$D/grab" 456 3 "$D/back" &
GRAB=$!
"$D/click" 1125 112 900               # the 磁盘和文件夹 breadcrumb pill
wait $GRAB
