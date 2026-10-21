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
