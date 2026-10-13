# Performance

NeferWL uses about 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change.

- Only changed areas are redrawn. Windows nobody sees get one frame callback per second.
- Fullscreen windows go straight to the display (direct scanout), with tearing for games. See `render.direct-scanout` and `render.tearing` in [Configuration](config.md#keys).
- A full frame at 2560×1600 takes about 0.16 ms on an RX 9070 XT and 3 ms on a Radeon 680M. Measure yours with `go test ./internal/adapters/vulkan -bench ComposeFullTarget` (`NEFERWL_VK_DEVICE` picks the GPU).
- NeferWL runs with `GOGC=100` and a 256 MiB soft memory limit. `GOGC` and `GOMEMLIMIT` from the environment override them.

## VRR

With a fullscreen game under VRR, each game frame is committed at least `render.vrr-flip-gap` (default 1 ms) after the previous flip. On amdgpu, committing earlier can hold the panel at its slowest refresh rate.

## Real-time priority

Output and input threads ask for real-time scheduling, which needs `CAP_SYS_NICE`. The Arch and Debian packages set it. For a local build:

```sh
sudo setcap cap_sys_nice+ep bin/neferwl
```

`performance.realtime = off` turns it off. With the capability set, the loader ignores `LD_PRELOAD` and `LD_LIBRARY_PATH`, and debuggers need root to attach.

## Profiling

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

Missed vblanks with `late_fences` mean the GPU was late. Without `late_fences` but with a large `max_commit_delay_ms`, the CPU was late.
