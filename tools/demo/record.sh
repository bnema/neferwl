#!/usr/bin/env bash
# Requires Go 1.27, rootless Docker, bash and ffmpeg. Run from any directory.
# KEEP_CAPTURE=1 preserves temporary captures on success; failures always clean up.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
for command in docker go ffmpeg; do
    command -v "$command" >/dev/null || { echo "Missing required command: $command" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo 'Docker daemon unavailable; start rootless Docker first.' >&2; exit 1; }
work=$(mktemp -d "${TMPDIR:-/tmp}/neferwl-demo.XXXXXX")
compositor='' launcher='' feeder='' success=''
container=neferwl-demo-$$
cleanup() {
    local status=$?
    trap - EXIT
    # launcher and feeder run with job control (set -m) in their own process
    # groups, so killing the group also stops their sleep/docker exec children.
    for pid in "$launcher" "$feeder"; do
        if [[ -n $pid ]]; then kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true; fi
    done
    if [[ -n $compositor ]]; then kill "$compositor" 2>/dev/null || true; fi
    docker rm -f "$container" >/dev/null 2>&1 || true
    for pid in "$launcher" "$feeder" "$compositor"; do
        if [[ -n $pid ]]; then wait "$pid" 2>/dev/null || true; fi
    done
    exec 3>&-
    if [[ $success != yes || ${KEEP_CAPTURE:-0} != 1 ]]; then
        rm -rf -- "$work"
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
printf 'Capture directory: %s\n' "$work"
mkdir -p "$work/shots" docs/media
docker build -t neferwl-demo -f tools/demo/Dockerfile tools/demo
CGO_ENABLED=0 go build -o "$work/neferwl" ./cmd/neferwl
state=$work/state
log=$state/neferwl/runs/headless/latest.log
mkfifo "$work/input"
# Hold both ends open so the FIFO cannot block compositor startup or produce
# premature EOF. No input is fed until Docker and the first app are ready.
exec 3<>"$work/input"
env -u NEFERWL_STATE -u NEFERWL_SLOT -u WAYLAND_DISPLAY XDG_STATE_HOME="$state" "$work/neferwl" \
    --backend=headless --size 1920x1080 --config tools/demo/config \
    --no-xwayland --screenshot "$work/shots" --input - --timeout 75s <"$work/input" &
compositor=$!
socket=''
for ((attempt = 0; attempt < 150; attempt++)); do
    kill -0 "$compositor" 2>/dev/null || { echo "Compositor exited before readiness; see $log" >&2; exit 1; }
    if [[ -f $log ]]; then
        display=$(sed -n '/"message":"listening"/s/.*"WAYLAND_DISPLAY":"\([^" ]*\)".*/\1/p' "$log" | tail -1)
        if [[ -n $display && -S $XDG_RUNTIME_DIR/${display##*/} ]]; then
            socket=$XDG_RUNTIME_DIR/${display##*/}
            break
        fi
    fi
    sleep 0.1
done
[[ -n $socket ]] || { echo "Timed out after 15s waiting for compositor socket; see $log" >&2; exit 1; }
# Container root maps to the host uid under rootless Docker. Grant demo access
# to this disposable socket only; Firefox rejects root with a demo-owned HOME.
docker run -d --name "$container" --hostname neferwl-demo --network none \
    -v "$socket:/run/wl/wayland-0" neferwl-demo
docker exec "$container" chmod 666 /run/wl/wayland-0
at() { local delay=$(($1 - SECONDS)); if ((delay > 0)); then sleep "$delay"; fi; }
launch_apps() {
    # This shell timeline shares t=0 with input.txt. Only keys live in input.txt;
    # Docker launches cannot be invoked by compositor input scripts.
    at 3
    docker exec -d --user demo "$container" dbus-run-session -- firefox --no-remote \
        --profile /home/demo/firefox file:///home/demo/site/index.html
    # btop's first per-core sample is bogus in this container. Let it collect
    # real deltas on workspace 2; input.txt brings it into view at t=17.
    at 5
    docker exec -d --user demo "$container" foot --app-id demo-monitor \
        --title 'Monitor / btop' /usr/local/bin/btop
    at 9
    docker exec -d --user demo "$container" dbus-run-session -- bash -c \
        'gsettings set org.xfce.mousepad.preferences.view use-default-monospace-font false; gsettings set org.xfce.mousepad.preferences.view font-name "JetBrainsMono Nerd Font 15"; gsettings set org.xfce.mousepad.preferences.window menubar-visible false; mousepad /home/demo/config'
    at 30
    docker exec -d --user demo "$container" foot --title 'Fonts / workspace 2' bash -c \
        'eza --tree --level=2 --icons --color=always /usr/share/fonts | head -30; exec sleep infinity'
}
docker exec -d --user demo "$container" foot --title 'System / NeferWL' bash -c \
        'fastfetch --logo small --structure OS:Kernel:WM:Terminal:Memory:Colors; eza -la --no-user --icons --color=always /etc | head -24; exec sleep infinity'
# The first app is launched before t=0. FIFO input and shell launches now
# share this epoch, independent of compositor/image/container startup time.
SECONDS=0
set -m
{ cat tools/demo/input.txt; printf '\nkey Ctrl+Alt+BackSpace\n'; } >&3 &
feeder=$!
launch_apps &
launcher=$!
set +m
# --timeout exits nonzero; accept only its explicit timeout log entry.
if ! wait "$compositor"; then
    grep -q '"reason":"timeout"' "$log" || exit 1
fi
compositor=
wait "$feeder"
feeder=
wait "$launcher"
launcher=
ffmpeg -hide_banner -loglevel warning -f concat -safe 0 \
    -i "$work/shots/frames.ffconcat" -vf 'fps=30,format=yuv420p' \
    -c:v libx264 -crf 23 -movflags +faststart docs/media/demo.mp4 -y
ffmpeg -hide_banner -loglevel warning -ss 7 -i docs/media/demo.mp4 \
    -frames:v 1 -update 1 docs/media/demo.png -y
success=yes
if [[ ${KEEP_CAPTURE:-0} == 1 ]]; then printf 'Captures kept: %s\n' "$work"; fi
printf 'Recorded: docs/media/demo.mp4 and docs/media/demo.png\n'
