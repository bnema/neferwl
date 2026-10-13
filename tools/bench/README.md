# Headless frame-cost bench

`bench.sh` measures NeferWL's CPU time and voluntary context switches per presented frame on a 5120x2160 headless output while chromium animates a small part of a page.

    tools/bench/bench.sh spin60.html 15   # CSS spinner, 60 fps
    tools/bench/bench.sh gif.html 15      # 32x32 GIF, 20 fps

Environment: `BENCH_X11=1` runs chromium through XWayland, `BENCH_DEBUG=<categories>` adds `--debug` categories, `BENCH_KEEP=1` leaves the processes running, `BENCH_PPROF=<addr>` sets the pprof address (default `localhost:6061`).
