#!/usr/bin/env bash
# Requires Go 1.27, rootless Docker, bash and ffmpeg. Run from any directory.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
work=$(mktemp -d "${TMPDIR:-/tmp}/neferwl-demo.XXXXXX")
printf 'Capture directory: %s\n' "$work"
mkdir -p "$work/shots" docs/media
compositor='' launcher='' container_started=''
cleanup() {
    if [[ -n $launcher ]]; then kill "$launcher" 2>/dev/null || true; fi
    if [[ -n $container_started ]]; then docker rm -f neferwl-demo >/dev/null 2>&1 || true; fi
    if [[ -n $compositor ]]; then kill "$compositor" 2>/dev/null || true; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Refuse to replace someone else's running container.
if [[ -n $(docker ps -aq --filter 'name=^/neferwl-demo$') ]]; then
    echo 'Container neferwl-demo already exists; remove it before recording.' >&2
    exit 1
fi
docker build -t neferwl-demo -f tools/demo/Dockerfile tools/demo
CGO_ENABLED=0 go build -o "$work/neferwl" ./cmd/neferwl
log=${XDG_STATE_HOME:-$HOME/.local/state}/neferwl/runs/headless/latest.log
env -u NEFERWL_STATE -u NEFERWL_SLOT -u WAYLAND_DISPLAY "$work/neferwl" \
    --backend=headless --size 1920x1080 --config tools/demo/config \
    --no-xwayland --screenshot "$work/shots" --input tools/demo/input.txt --timeout 45s &
compositor=$!
SECONDS=0
# The host starts only the compositor; all apps and their content stay in Docker.
# A fixed startup allowance avoids reading a previous run's socket from the log.
sleep 1
kill -0 "$compositor"
display=$(sed -n '/"message":"listening"/s/.*"WAYLAND_DISPLAY":"\([^"]*\)".*/\1/p' "$log" | tail -1)
[[ -n $display ]] || { echo "No listening socket in $log" >&2; exit 1; }
socket=$XDG_RUNTIME_DIR/${display##*/}
[[ -S $socket ]] || { echo "Not a socket: $socket" >&2; exit 1; }
# Container root maps to the host uid under rootless Docker. Grant demo access
# to this disposable socket only; Firefox rejects root with a demo-owned HOME.
docker run -d --name neferwl-demo --hostname neferwl-demo --network none \
    -v "$socket:/run/wl/wayland-0" neferwl-demo
container_started=yes
docker exec neferwl-demo chmod 666 /run/wl/wayland-0
at() { local delay=$(($1 - SECONDS)); if ((delay > 0)); then sleep "$delay"; fi; }
launch_apps() {
    # This shell timeline shares t=0 with input.txt. Only keys live in input.txt;
    # Docker launches cannot be invoked by compositor input scripts.
    docker exec -d --user demo neferwl-demo foot --title 'System / NeferWL' bash -c \
        'fastfetch --logo small --structure OS:Kernel:WM:Terminal:Memory:Colors; eza -la --no-user --icons --color=always /etc | head -24; exec sleep infinity'
    at 3
    docker exec -d --user demo neferwl-demo dbus-run-session -- firefox --no-remote \
        --profile /home/demo/firefox file:///home/demo/site/index.html
    # btop's first per-core sample is bogus in this container. Let it collect
    # real deltas on workspace 2; input.txt brings it into view at t=17.
    at 5
    docker exec -d --user demo neferwl-demo foot --app-id demo-monitor \
        --title 'Monitor / btop' /usr/local/bin/btop
    at 9
    docker exec -d --user demo neferwl-demo dbus-run-session -- bash -c \
        'gsettings set org.xfce.mousepad.preferences.view use-default-monospace-font false; gsettings set org.xfce.mousepad.preferences.view font-name "JetBrainsMono Nerd Font 15"; gsettings set org.xfce.mousepad.preferences.window menubar-visible false; mousepad /home/demo/config'
    at 30
    docker exec -d --user demo neferwl-demo foot --title 'Fonts / workspace 2' bash -c \
        'eza --tree --level=2 --icons --color=always /usr/share/fonts | head -30; exec sleep infinity'
}
launch_apps &
launcher=$!
# --timeout exits nonzero; accept only its explicit timeout log entry.
if ! wait "$compositor"; then
    grep -q '"reason":"timeout"' "$log" || exit 1
fi
compositor=
wait "$launcher"
launcher=
ffmpeg -hide_banner -loglevel warning -f concat -safe 0 \
    -i "$work/shots/frames.ffconcat" -vf 'fps=30,format=yuv420p' \
    -c:v libx264 -crf 23 -movflags +faststart docs/media/demo.mp4 -y
ffmpeg -hide_banner -loglevel warning -ss 7 -i docs/media/demo.mp4 \
    -frames:v 1 -update 1 docs/media/demo.png -y
printf 'Recorded: %s\n' "$work"
