#!/usr/bin/env bash
# No-client headless idle budget, after stats/trim warmup.
set -euo pipefail
secs=${1:?usage: idle-budget.sh <seconds>}
[[ $secs =~ ^[1-9][0-9]*$ ]] || { echo 'seconds must be a positive integer' >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
root=${BENCH_ROOT:-$(cd "$here/../.." && pwd)}
tmp=$(mktemp -d)
pid=
cleanup() {
 if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
 rm -rf "$tmp"
}
trap cleanup EXIT
(cd "$root" && CGO_ENABLED=0 go build -o "$tmp/nwl" ./cmd/neferwl)
mkdir -p "$tmp/rt" "$tmp/cfg" "$tmp/state" "$tmp/cache" "$tmp/data"
chmod 700 "$tmp/rt"
env XDG_RUNTIME_DIR="$tmp/rt" XDG_CONFIG_HOME="$tmp/cfg" XDG_STATE_HOME="$tmp/state" XDG_CACHE_HOME="$tmp/cache" XDG_DATA_HOME="$tmp/data" WAYLAND_DISPLAY= \
 "$tmp/nwl" --backend=headless --size=1920x1080 --no-terminal --no-xwayland --debug=render >"$tmp/out" 2>&1 </dev/null &
pid=$!
log=$tmp/state/neferwl/runs/headless/latest.log
for _ in $(seq 50); do [ -S "$tmp/rt/wayland-1" ] && break; sleep 0.1; done
[ -S "$tmp/rt/wayland-1" ] || { cat "$tmp/out" >&2; exit 1; }
log=$(find "$tmp/state/neferwl" -name latest.log -print -quit)
[ -n "$log" ] && [ -r "$log" ] || { echo 'no private compositor log' >&2; exit 1; }
sleep 70
cs() { cat /proc/"$pid"/task/*/status | awk '/^voluntary_ctxt_switches:/{v+=$2} END{printf "%.0f\n",v}'; }
cpu() { cat /proc/"$pid"/task/*/schedstat | awk '{v+=$1} END{printf "%.0f\n",v}'; }
count() { grep -c "\"message\":\"$1\"" "$log" || true; }
c0=$(cs); t0=$(cpu); f0=$(count frame); s0=$(count stats)
sleep "$secs"
c1=$(cs); t1=$(cpu); f1=$(count frame); s1=$(count stats)
python3 - "$((f1-f0))" "$((c1-c0))" "$((t1-t0))" "$((s1-s0))" "$secs" <<'PY'
import sys
f,c,t,stats,s = map(int,sys.argv[1:])
print(f'frames={f} cs/s={c/s:.3f} cpu%={t/1e9/s*100:.4f} stats_lines={stats}')
sys.exit(1 if f > 0 else 0)
PY
