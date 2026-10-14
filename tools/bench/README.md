# Headless frame-cost bench

`bench.sh` measures NeferWL's CPU time and voluntary context switches per presented frame on a 5120x2160 headless output while chromium animates a small part of a page.

Requires `go`, `chromium` and `python3`. Frames are counted from the renderer's `frame` debug entries, which the script turns on.

    tools/bench/bench.sh spin60.html 15   # CSS spinner, 60 fps
    tools/bench/bench.sh gif.html 15      # 32x32 GIF, 20 fps

Environment: `BENCH_X11=1` runs chromium through XWayland, `BENCH_DEBUG=<categories>` adds `--debug` categories, `BENCH_KEEP=1` leaves the processes running, `BENCH_PPROF=<addr>` sets the pprof address (default `localhost:6061`).

`idle.html` is a static page: run it with `BENCH_IDLE=1 tools/bench/bench.sh idle.html 15` (no frame is required). The output also reports `cs/s` and, for the thread with the most voluntary switches (usually the Go runtime's sysmon), its scheduler wakes per second (`top_wakes/s`, schedstat field 3). `BENCH_ROOT=<worktree>` builds neferwl from another worktree for A/B runs.
