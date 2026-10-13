#!/usr/bin/env bash
# Measures NeferWL's CPU cost per presented frame on the headless backend
# while chromium animates a small region of a page.
#
# usage: tools/bench/bench.sh <page.html> <seconds>
# env:   BENCH_X11=1      run chromium through XWayland instead of Wayland
#        BENCH_DEBUG=cat  extra --debug categories (comma separated)
#        BENCH_KEEP=1     leave neferwl and chromium running (prints PIDs)
#        BENCH_PPROF=addr pprof listen address (default localhost:6061)
set -euo pipefail

page=${1:?usage: bench.sh <page.html> <seconds>}
secs=${2:?usage: bench.sh <page.html> <seconds>}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
tmp=$(mktemp -d)
pids=()

cleanup() {
	if [ -z "${BENCH_KEEP:-}" ]; then
		for p in "${pids[@]}"; do kill -- "-$p" 2>/dev/null || true; done
		sleep 1
		rm -rf "$tmp"
	fi
}
trap cleanup EXIT

(cd "$root" && CGO_ENABLED=0 go build -o "$tmp/nwl" ./cmd/neferwl)
mkdir -p "$tmp/rt" "$tmp/cfg" "$tmp/state"
chmod 700 "$tmp/rt"

debug=render${BENCH_DEBUG:+,$BENCH_DEBUG}
xflag=--no-xwayland
[ -n "${BENCH_X11:-}" ] && xflag=
env XDG_RUNTIME_DIR="$tmp/rt" XDG_CONFIG_HOME="$tmp/cfg" XDG_STATE_HOME="$tmp/state" WAYLAND_DISPLAY= \
	setsid "$tmp/nwl" --backend=headless --size=5120x2160 --no-terminal $xflag \
	--debug="$debug" --pprof="${BENCH_PPROF:-localhost:6061}" >"$tmp/nwl.out" 2>&1 </dev/null &
nwl=$!
pids+=("$nwl")
log=$tmp/state/neferwl/runs/headless/latest.log
for _ in $(seq 50); do [ -S "$tmp/rt/wayland-1" ] && break; sleep 0.1; done

if [ -n "${BENCH_X11:-}" ]; then
	disp=
	for _ in $(seq 50); do
		disp=$(grep -o '"DISPLAY":"[^"]*"' "$log" 2>/dev/null | head -1 | cut -d'"' -f4) || true
		[ -n "$disp" ] && break
		sleep 0.1
	done
	[ -n "$disp" ] || { echo "no X11 display in log" >&2; exit 1; }
	env XDG_RUNTIME_DIR="$tmp/rt" WAYLAND_DISPLAY= DISPLAY="$disp" setsid chromium --ozone-platform=x11 \
		--user-data-dir="$tmp/chr" --no-first-run --app="file://$here/$page" >"$tmp/chr.out" 2>&1 </dev/null &
else
	env XDG_RUNTIME_DIR="$tmp/rt" WAYLAND_DISPLAY=wayland-1 setsid chromium --ozone-platform=wayland \
		--user-data-dir="$tmp/chr" --no-first-run --app="file://$here/$page" >"$tmp/chr.out" 2>&1 </dev/null &
fi
pids+=("$!")
sleep 8

cs() { cat /proc/"$nwl"/task/*/status | awk '/^voluntary_ctxt/{v+=$2} END{print v}'; }
ticks() { awk '{print $14+$15}' /proc/"$nwl"/stat; }
frames() { grep -c '"message":"frame"' "$log" || true; }

t0=$(ticks); f0=$(frames); c0=$(cs)
sleep "$secs"
t1=$(ticks); f1=$(frames); c1=$(cs)

hz=$(getconf CLK_TCK)
python3 - "$((t1 - t0))" "$((f1 - f0))" "$((c1 - c0))" "$secs" "$hz" "$page" <<'EOF'
import sys
t, f, c, s, hz, page = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3]), float(sys.argv[4]), int(sys.argv[5]), sys.argv[6]
cpu = t / hz / s * 100
fr = max(f, 1)
print(f"{page} cpu%={cpu:.2f} fps={f / s:.1f} ms/frame={t / hz * 1000 / fr:.3f} cs/frame={c / fr:.0f}")
EOF

if [ -n "${BENCH_KEEP:-}" ]; then
	echo "kept: dir=$tmp pids=${pids[*]} log=$log"
fi
