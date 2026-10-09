# Performance

NeferWL uses about 40–80 MB of RAM with two 4K monitors and almost no CPU while the screen does not change.

## How it stays light

- **No reflection across the C boundary.** libwayland and Vulkan calls go through fixed-arity `purego` syscalls and callbacks, with zero allocations per call.
- **Few allocations per frame.** Frame callbacks, flips, Vulkan submissions and surface updates reuse buffers; scene publication and `Render` stay within small fixed allocation counts. `make perf-check` fails if a hot path allocates more than its budget.
- **Paced frame callbacks.** Clients are told to draw on each output's flip or refresh, so a 165 Hz output paces them at 165 Hz. Cursors and surfaces without an output get 60 Hz. The pacer sleeps while no callback waits.
- **Hidden windows rest.** A window nobody sees (another workspace, a hidden stash, scrolled off screen, under a fullscreen window) gets frame callbacks once per second, and the `suspended` state if its client supports xdg-shell v6. A captured window keeps its output's rate.
- **Redraw only what changed.** An unchanged output is not recomposed. A content change redraws only its damage, and a moving window only its old and new area. A change to layers, scale, transform, dim or capture redraws the whole output. Resizing one of three columns at 2560×1600 redraws about 0.2 M pixels per frame instead of 4.1 M.
- **Direct scanout.** A fullscreen buffer goes straight to the display, with tearing for games, when `render.direct-scanout` and `render.tearing` allow it (see [Configuration](config.md#keys)).
- **Cheap animations** (experimental, off by default: `animations = on`). Springs reuse the composition pass: no extra GPU pass, blur or texture copy. Clients get one configure, at the final size. A window that is fading or zooming is composed, not scanned out directly.
- **Compressed render targets.** Output images use the best modifier the display plane accepts (AMD DCC, then tiled, then linear). The log line `zero-copy output` shows the choice.
- **Bounded capture.** At most 32 capture requests are in flight. A new request is never queued: wlr-screencopy fails it at once, and ext-image-copy retries for up to 1 s, then fails. Display capture uses up to 128 MiB of host memory per output (two 5K frames; larger outputs cannot be captured). Capturing a hidden workspace adds a separate renderer with its own 128 MiB, freed when idle. The capture indicator also costs composition; see [Screen capture](capture.md#the-indicator).
- **GC settings.** The Go heap is a few MB because client buffers live in shared and GPU memory. NeferWL runs with `GOGC=100` and a 256 MiB soft memory limit; `GOGC` and `GOMEMLIMIT` from the environment override them.

A full composed frame at 2560×1600 costs about 0.16 ms on an RX 9070 XT, 1.4 ms on a Raphael iGPU and 3 ms on a Radeon 680M, against 11.1 ms per frame at 90 Hz. To measure a GPU yourself: `go test ./internal/adapters/vulkan -bench ComposeFullTarget` (`NEFERWL_VK_DEVICE` picks the GPU).

## VRR

With a fullscreen game under VRR:

- The cursor moves with the game's next frame instead of forcing its own refresh, which would drop the panel to its slowest rate. If the game stops drawing, the cursor updates alone at 24 Hz.
- A game frame commits at least `render.vrr-flip-gap` (default 1 ms) after the previous flip. On amdgpu, committing earlier can hold the panel at its slowest refresh (48 Hz on the measured panel) while the game renders at 150 fps. Measurements are in `internal/adapters/drm/vrr_flip_gap.go`.

## Real-time priority

Output and input threads request real-time scheduling, which needs `CAP_SYS_NICE`. The Arch and Debian packages set it on `/usr/bin/neferwl`. Apps started by NeferWL do not inherit it. The Vulkan queue also requests realtime GPU priority, falling back to high.

- Turn off real-time threads: `performance.realtime = off`. The GPU queue priority is still requested.
- Grant the capability to a local build: `sudo setcap cap_sys_nice+ep bin/neferwl`.

With the capability, the loader ignores `LD_PRELOAD` and `LD_LIBRARY_PATH`, and debuggers need root to attach.

## Measuring

Profile a running compositor from another TTY or over SSH:

```sh
# CPU, Go and C frames
perf record -g -p (pgrep -f bin/neferwl) -- sleep 8
perf report --children --percent-limit 1

# Allocations per Go line
neferwl --backend=drm --pprof=localhost:6060
go tool pprof -sample_index=alloc_objects -top http://localhost:6060/debug/pprof/heap

# One line per GC cycle
GODEBUG=gctrace=1 neferwl --backend=drm
```

`make tty` serves pprof on `localhost:6060` (`make tty PPROF=` turns it off). It also serves `/debug/pprof/profile` and `/debug/pprof/trace`.

Logs are in `$XDG_STATE_HOME/neferwl/runs/<backend>/` (default `~/.local/state`), with `latest.log` pointing at the newest run. Each backend keeps 20 runs. `--debug=all` leaves out the noisy categories `drm-flip`, `input-motion` and `input-keys`; name them to turn them on.

### Frame stats

Every 10 s, each output logs a `stats` entry for the last interval. The main fields:

| Field | Meaning |
|---|---|
| `missed_vblanks` | Due frames that flipped more than 1.5 refresh periods after the previous one. Not counted under VRR |
| `max_flip_interval_ms` | Longest gap between two flips of due frames, VRR included. Restarts after a modeset |
| `max_commit_delay_ms` | Longest wait from "frame due" to commit |
| `max_flip_to_read_ms` | Longest wait from a flip to NeferWL reading its event |
| `late_fences` | Missed vblanks where the GPU finished too late. Needs `--debug=drm-flip` |
| `redrawn_pixels` | Pixels the renderer drew |

It also counts `frames`, `flips`, `pending`, `cursor_moves` and `cursor_commits`.

To find the cause of missed vblanks:

- With `late_fences`: the GPU composition was late.
- Without `late_fences` but with a large `max_commit_delay_ms`: the commit was late (CPU work or a late event read).

Few `missed_vblanks` mean flips were on time only if `max_commit_delay_ms` is also small.

`--debug=drm-flip` (or `make tty TTY_DEBUG=drm-flip`) logs every flip as a `flip` entry with `commit_to_flip_ms`, `flip_interval_ms`, `fence_ready_at_commit`, and, when the client fence reports it, `commit_to_fence_ms` and `fence_to_flip_ms`. A negative `commit_to_fence_ms` means the client finished before the commit. `input stats` shows coalesced pointer motion and the longest wait for core.

## Reference: 4K HDR video with tiled subsurfaces

A browser playing 4K HDR video with 65 subsurfaces committing every frame, measured with `perf record -g` for 8 s on the DRM backend:

| Metric | Before (issue #19) | 2026-09-27 |
|---|---|---|
| CPU | 20–30 % | 13–14 % |
| GC (`gcBgMarkWorker`) | 20 % | 8.7 % |
| Go → C reflection | 46.7 % | 0 |
| C → Go reflection | ~30 % | 0 |
| `Render` allocations per frame | 1321 | 10 |
| GC cycles on the playback test | 13 (91 ms) | 2 (3 ms) |

The remaining CPU time is real work: surface commits and fifo logic (~23 %), C calls (~5 %) and the Go scheduler (~8 %).
