# Profiling

Run these from another TTY or over SSH:

```sh
# CPU, Go and C frames
perf record -g -p (pgrep -f bin/neferwl) -- sleep 8
perf report --children --percent-limit 1

# Allocations
neferwl --backend=drm --pprof=localhost:6060
go tool pprof -sample_index=alloc_objects -top http://localhost:6060/debug/pprof/heap

# One line per GC cycle
GODEBUG=gctrace=1 neferwl --backend=drm
```

`make tty` serves pprof on `localhost:6060` by default (`make tty PPROF=` turns it off).

Logs are in `~/.local/state/neferwl/runs/<backend>/`, with `latest.log` pointing at the newest run. `--debug=all` enables every category except the noisy `drm-flip`, `input-motion`, `input-keys`, `wayland-damage` (one `client damage` entry per client commit) and `render-damage` (one `frame damage` entry per rendered frame); name them to turn them on.

## Frame stats

Every 10 s, each output logs a `stats` entry:

| Field | Meaning |
|---|---|
| `missed_vblanks` | Frames that flipped more than 1.5 refresh periods late (not counted under VRR) |
| `max_flip_interval_ms` | Longest gap between two flips |
| `max_commit_delay_ms` | Longest wait from "frame due" to commit |
| `late_fences` | Missed vblanks where the GPU finished too late (needs `--debug=drm-flip`) |
| `redrawn_pixels` | Pixels drawn |

If outputs report flips faster than the display loop reads them, wayland merges them and logs a warning with `merged` at most every 10 s.

Missed vblanks with `late_fences` mean the GPU was late. Without `late_fences` but with a large `max_commit_delay_ms`, the CPU was late.
