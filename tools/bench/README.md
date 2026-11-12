# Headless frame-cost bench

`bench.sh` measures NeferWL's CPU time and voluntary context switches per presented frame on a 5120x2160 headless output while chromium animates a small part of a page.

Requires `go`, `chromium` and `python3`. Frames are counted from the renderer's `frame` debug entries, which the script turns on.

    tools/bench/bench.sh spin60.html 15   # CSS spinner, 60 fps
    tools/bench/bench.sh gif.html 15      # 32x32 GIF, 20 fps

`BENCH_SIZE` sets output sizes (default `5120x2160`); use
`BENCH_SIZE=5120x2160,1920x1080` for a two-output baseline.

Environment: `BENCH_X11=1` runs chromium through XWayland, `BENCH_DEBUG=<categories>` adds `--debug` categories, `BENCH_KEEP=1` leaves the processes running, `BENCH_PPROF=<addr>` sets the pprof address (default `localhost:6061`).

`idle.html` is a static page: run it with `BENCH_IDLE=1 tools/bench/bench.sh idle.html 15` (no frame is required). The output also reports `cs/s` and, for the thread with the most voluntary switches (usually the Go runtime's sysmon), its scheduler wakes per second (`top_wakes/s`, schedstat field 3). `BENCH_ROOT=<worktree>` builds neferwl from another worktree for A/B runs.

## Idle and GPU budgets

`tools/bench/idle-budget.sh 60` builds from `BENCH_ROOT` (default this worktree),
starts a private 1920x1080 headless compositor without clients, waits 70 seconds
for initial stats/trim work, then reports `frames= cs/s= cpu%= stats_lines=`.
It exits nonzero if frames are rendered during sampling and terminates only its
own compositor. Compare repeated runs under similar host load (target ±15% cs/s).

`tools/bench/gpumem.sh <pid>` sums DRM VRAM/GTT KiB and cumulative engine ns,
deduplicating fdinfo entries by DRM device and client ID. It needs a same-user,
non-setcap binary: use a dev build or `BENCH_KEEP=1 tools/bench/bench.sh spin60.html 15`,
then pass the printed compositor PID. Do not use `/usr/bin/neferwl` (its setcap
protection makes fdinfo unreadable). Record host/GPU load alongside results;
engine time is cumulative, not utilization. A process with no DRM clients reports zeros.
After a kept run, terminate only the process groups printed by that run.
