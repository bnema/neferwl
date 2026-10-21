#!/usr/bin/env bash
# Requires Go 1.27, rootless Docker, bash, ffmpeg and network access.
# Run from any directory. The image holds the compositor's runtime (tesseract,
# AT-SPI, a Mesa Vulkan driver) and the apps; the host builds the binary.
# KEEP_CAPTURE=1 preserves temporary captures on success; failures always clean up.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
for command in docker go ffmpeg; do
    command -v "$command" >/dev/null || { echo "Missing required command: $command" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo 'Docker daemon unavailable; start rootless Docker first.' >&2; exit 1; }
work=$(mktemp -d "${TMPDIR:-/tmp}/neferwl-demo.XXXXXX")
launcher='' feeder='' success=''
container=neferwl-demo-$$
cleanup() {
    local status=$?
    trap - EXIT
    # launcher and feeder run with job control (set -m) in their own process
    # groups, so killing the group also stops their sleep/docker exec children.
    for pid in "$launcher" "$feeder"; do
        if [[ -n $pid ]]; then kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true; fi
    done
    docker rm -f "$container" >/dev/null 2>&1 || true
    for pid in "$launcher" "$feeder"; do
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
mkdir -p "$work/shots" "$work/state" docs/media
docker build -t neferwl-demo -f tools/demo/Dockerfile tools/demo
CGO_ENABLED=0 go build -o "$work/neferwl" ./cmd/neferwl
log=$work/state/neferwl/runs/headless/latest.log
mkfifo "$work/input"
# Hold both ends open so the FIFO cannot block compositor startup or produce
# premature EOF. No input is fed until the first app is ready.
exec 3<>"$work/input"
# The compositor is the container's main process, so the container ends with
# it. Root in a rootless container is the host user: the bind-mounted work
# directory (binary, FIFO, frames, log, hint dumps) stays owned by the host user.
# The GPU render node gives the compositor the sync files that hint scans need.
gpu=()
if [[ -d /dev/dri ]]; then gpu=(--device /dev/dri); else echo 'No /dev/dri: hint scans will fail.' >&2; fi
docker run -d --name "$container" --hostname neferwl-demo "${gpu[@]}" \
    -e XDG_STATE_HOME=/work/state -e NEFERWL_HINT_DEBUG=1 \
    -v "$work:/work" -v "$work/neferwl:/usr/local/bin/neferwl:ro" neferwl-demo \
    --backend=headless --size 1920x1080 --config /root/config --no-xwayland \
    --screenshot /work/shots --input /work/input --timeout 120s >/dev/null
display=''
for ((attempt = 0; attempt < 300; attempt++)); do
    [[ $(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null) == true ]] ||
        { echo "Compositor exited before readiness; see $log" >&2; docker logs "$container" >&2 || true; exit 1; }
    if [[ -f $log ]]; then
        display=$(sed -n '/"message":"listening"/s/.*"WAYLAND_DISPLAY":"\([^" ]*\)".*/\1/p' "$log" | tail -1)
        [[ -n $display ]] && break
    fi
    sleep 0.1
done
[[ -n $display ]] || { echo "Timed out after 30s waiting for the compositor; see $log" >&2; exit 1; }
# Mousepad runs as another user (see launch_apps) and needs the socket.
docker exec "$container" chmod 666 "/run/wl/${display##*/}"
app() { docker exec -d -e "WAYLAND_DISPLAY=${display##*/}" "$container" "$@"; }
at() { local delay=$(($1 - SECONDS)); if ((delay > 0)); then sleep "$delay"; fi; }
launch_apps() {
    # This shell timeline shares t=0 with input.txt. Only keys live in input.txt;
    # app launches cannot be invoked by compositor input scripts.
    at 3
    app firefox --no-remote --profile /root/firefox 'https://duckduckgo.com/?q=wayland+compositor'
    # btop's first per-core sample is bogus in this container. Let it collect
    # real deltas on workspace 2; input.txt brings it into view at t=17.
    at 5
    app foot --app-id demo-monitor --title 'Monitor / btop' /usr/local/bin/btop
    at 9
    # Mousepad runs as demo (it warns about root), with a file-backed
    # GSettings and no session bus (it needs the root-owned bus otherwise).
    app setpriv --reuid=1000 --regid=1000 --clear-groups env HOME=/home/demo \
        GSETTINGS_BACKEND=keyfile DBUS_SESSION_BUS_ADDRESS=disabled: \
        mousepad --disable-server /home/demo/config
    at 30
    app foot --title 'Fonts / workspace 2' bash -c \
        'eza --tree --level=2 --icons --color=always /usr/share/fonts | head -30; exec sleep infinity'
}
feed() {
    # Forwards input.txt to the compositor in real time. `sleep` lines are
    # served here (not forwarded), so a host-side step can sit between two compositor steps:
    # `hint-click <word>...` types the label that hint mode gave the link
    # showing one of the words, from the dump of the scan the last `hint` bind
    # started (demo-hint-label reproduces the labelling of internal/core/hint.go).
    local line cmd d mark label
    while IFS= read -r line || [[ -n $line ]]; do
        cmd=${line%% *}
        case $cmd in
        '' | '#'*) ;;
        sleep)
            d=${line#sleep }
            if [[ $d == *ms ]]; then d=$(printf '%d.%03d' $((${d%ms} / 1000)) $((${d%ms} % 1000))); fi
            sleep "$d" ;;
        hint-click)
            # shellcheck disable=SC2086
            label=$(docker exec "$container" demo-hint-label "$mark" ${line#hint-click }) ||
                { echo "No hint label for: ${line#hint-click }" >&2; docker kill "$container" >/dev/null; return 1; }
            echo "Hint label for ${line#hint-click }: $label" >&2
            printf 'type %s\n' "$label" ;;
        *)
            [[ $line == 'key Super+g' ]] && mark=$(date +%s.%N)
            printf '%s\n' "$line" ;;
        esac
    done <tools/demo/input.txt
    printf 'key Ctrl+Alt+BackSpace\n'
}
app foot --title 'System / NeferWL' bash -c \
    'fastfetch --logo small --structure OS:Kernel:WM:Terminal:Memory:Colors; eza -la --no-user --icons --color=always /etc | head -24
    # A bare shell for the end of input.txt: no prompt, no echo, no line editing.
    stty -echo; exec env PS1= bash --norc --noediting'
# The first app is launched before t=0. FIFO input and shell launches now
# share this epoch, independent of compositor/image/container startup time.
SECONDS=0
set -m
feed >&3 &
feeder=$!
launch_apps &
launcher=$!
set +m
# The compositor ends with the quit key at the end of input.txt, or at --timeout.
status=$(docker wait "$container")
if [[ $status != 0 ]]; then
    grep -q '"reason":"timeout"' "$log" || { echo "Compositor failed (exit $status); see $log" >&2; docker logs "$container" >&2 || true; exit 1; }
fi
wait "$feeder" || { echo 'Scripted input failed.' >&2; exit 1; }
feeder=
wait "$launcher"
launcher=
# Hint mode needs both sources: the accessibility tree (Firefox) and OCR (terminal).
if grep -qE 'OCR unavailable|English OCR data missing' "$log"; then echo 'tesseract is missing in the image.' >&2; exit 1; fi
grep -qE '"targets":[1-9][0-9]*,.*"message":"hint scan"' "$log" ||
    { echo 'No hint scan found targets:' >&2; grep -E '"component":"(hints|atspi)"' "$log" | cut -c1-300 >&2; exit 1; }
grep '"message":"hint scan"' "$log" | sed -E 's/.*"atspi_targets":([0-9]+).*"ocr_links":([0-9]+).*"targets":([0-9]+).*/hint scan: \1 accessibility + \2 OCR links = \3 targets/' | tail -3
ffmpeg -hide_banner -loglevel warning -f concat -safe 0 \
    -i "$work/shots/frames.ffconcat" -vf 'fps=30,format=yuv420p' \
    -c:v libx264 -crf 23 -movflags +faststart docs/media/demo.mp4 -y
ffmpeg -hide_banner -loglevel warning -ss 8 -i docs/media/demo.mp4 \
    -frames:v 1 -update 1 docs/media/demo.png -y
success=yes
if [[ ${KEEP_CAPTURE:-0} == 1 ]]; then printf 'Captures kept: %s\n' "$work"; fi
printf 'Recorded: docs/media/demo.mp4 and docs/media/demo.png\n'
