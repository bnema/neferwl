#!/usr/bin/env bash
# Requires Go 1.27, foot 1.28+, bash, fastfetch and ffmpeg; run from any directory.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
work=$(mktemp -d "${TMPDIR:-/tmp}/neferwl-demo.XXXXXX")
# Keep the temporary capture for inspection; every run gets a fresh shots dir.
printf 'Capture directory: %s\n' "$work"
mkdir -p "$work/shots" docs/media
CGO_ENABLED=0 go build -o "$work/neferwl" ./cmd/neferwl
env -u NEFERWL_STATE -u NEFERWL_SLOT "$work/neferwl" \
    --backend=headless --size 1920x1080 --config tools/demo/config \
    --no-xwayland --screenshot "$work/shots" --input tools/demo/input.txt --timeout 35s
ffmpeg -hide_banner -loglevel warning -f concat -safe 0 \
    -i "$work/shots/frames.ffconcat" -vf 'fps=60,format=yuv420p' \
    -c:v libx264 -crf 23 -movflags +faststart docs/media/demo.mp4 -y
ffmpeg -hide_banner -loglevel warning -ss 15.5 -i docs/media/demo.mp4 \
    -frames:v 1 -update 1 docs/media/demo.png -y
# Keep the optional, smaller preview only when it fits the README asset budget.
ffmpeg -hide_banner -loglevel warning -i docs/media/demo.mp4 \
    -vf 'fps=10,scale=640:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse' \
    "$work/demo.gif" -y
if (( $(wc -c < "$work/demo.gif") < 5000000 )); then
    cp "$work/demo.gif" docs/media/demo.gif
else
    printf 'GIF exceeds 5 MB; skipped (candidate: %s/demo.gif).\n' "$work"
fi
