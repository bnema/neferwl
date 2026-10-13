#!/usr/bin/env bash
# Measures NeferWL's CPU cost per presented frame on the headless backend
# while chromium animates a small region of a page.
#
# usage: tools/bench/bench.sh <page.html> <seconds>
# env:   BENCH_X11=1      run chromium through XWayland instead of Wayland
#        BENCH_DEBUG=cat  extra --debug categories (comma separated)
#        BENCH_KEEP=1     leave neferwl and chromium running (prints PIDs)
#        BENCH_PPROF=addr pprof listen address (default localhost:6061)
#        BENCH_ROOT=dir   build neferwl from another worktree (A/B runs)
#        BENCH_IDLE=1     static page: no frames expected, per-second numbers only
set -euo pipefail

page=${1:?usage: bench.sh <page.html> <seconds>}
secs=${2:?usage: bench.sh <page.html> <seconds>}
here=$(cd "$(dirname "$0")" && pwd)
root=${BENCH_ROOT:-$(cd "$here/../.." && pwd)}
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
if [ ! -S "$tmp/rt/wayland-1" ]; then
	echo "neferwl did not start: no wayland-1 socket; its output:" >&2
	cat "$tmp/nwl.out" >&2
	exit 1
fi

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
# CPU time in ns summed over threads (schedstat is finer than stat's ticks).
cpu_ns() { cat /proc/"$nwl"/task/*/schedstat | awk '{v+=$1} END{printf "%d\n", v}'; }
frames() { grep -c '"message":"frame"' "$log" || true; }

# Per-thread "tid sched_in_count voluntary_switches" (schedstat field 3 = times scheduled in).
threads() { for d in /proc/"$nwl"/task/*; do
	echo "${d##*/} $(awk '{print $3}' "$d/schedstat" 2>/dev/null) $(awk '/^voluntary_ctxt/{print $2}' "$d/status" 2>/dev/null)"
done; }

th0=$(threads); t0=$(cpu_ns); f0=$(frames); c0=$(cs)
sleep "$secs"
th1=$(threads); t1=$(cpu_ns); f1=$(frames); c1=$(cs)

if [ -z "${BENCH_IDLE:-}" ] && [ "$((f1 - f0))" -le 0 ]; then
	echo "no frame presented in ${secs}s: the page did not animate or neferwl stalled" >&2
	tail -n 20 "$tmp/nwl.out" >&2
	exit 1
fi

python3 - "$((t1 - t0))" "$((f1 - f0))" "$((c1 - c0))" "$secs" "$page" "$th0" "$th1" <<'EOF'
import sys
t, f, c, s, page = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3]), float(sys.argv[4]), sys.argv[5]
def parse(x):
    return {l.split()[0]: tuple(int(v) for v in l.split()[1:]) for l in x.splitlines() if len(l.split()) == 3}
a, b = parse(sys.argv[6]), parse(sys.argv[7])
d = {k: (b[k][0] - a.get(k, (0, 0))[0], b[k][1] - a.get(k, (0, 0))[1]) for k in b}
top = max(d, key=lambda k: d[k][1])
cpu = t / 1e9 / s * 100
fr = max(f, 1)
print(f"{page} cpu%={cpu:.2f} fps={f / s:.1f} ms/frame={t / 1e6 / fr:.3f} cs/frame={c / fr:.0f} cs/s={c / s:.0f} top_tid={top} top_wakes/s={d[top][0] / s:.0f} top_vol/s={d[top][1] / s:.0f}")
EOF

if [ -n "${BENCH_KEEP:-}" ]; then
	echo "kept: dir=$tmp pids=${pids[*]} log=$log"
fi
