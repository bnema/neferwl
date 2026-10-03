# Performance

How NeferWL keeps its CPU and memory use low, how to measure it, and the reference numbers to compare against.

## Design

- **No reflection on the FFI boundary.** libwayland and Vulkan are called through `purego.Syscall6/Syscall15` (fixed arity, zero allocation) and C calls back into Go through `purego.NewCallbackInts`. The Vulkan dispatch tables are generated methods; the libwayland wrapper caches resource id, version and client, and precomputes request argument counts.
- **Allocation-free steady-state presentation.** Frame callbacks, presentation reports, DRM flips, Vulkan submissions and surface updates reuse owner-goroutine scratch or immutable snapshots. Surface updates (one per `wl_surface.commit`) are recycled once no queued update references them.
- **Cursor rides on game frames under VRR.** While a fullscreen game drives VRR, a cursor move waits for the game's next frame instead of its own commit: a cursor-only commit refreshes the panel at its slowest rate (~21 ms at 48 Hz) and delays the next game frame by as much. If the game stops drawing, the cursor still commits alone at 24 Hz.
- **Game frames keep a short gap after each VRR flip.** On amdgpu, a flip committed right after the previous flip event can miss the early refresh and wait for the panel's slowest rate (~21 ms at 48 Hz); the next frame then lands at the same point, and the screen stays at 48 Hz while the game renders at 150 fps. Game frames under VRR therefore commit at least `render.vrr-flip-gap` (default 1 ms) after the previous flip. `internal/adapters/drm/vrr_flip_gap.go` has the measurements.
- **Bounded capture.** Protocol requests and sync-file handles still need small allocations; pixel storage and worker batches are reused. At most 32 protocol requests are outstanding globally, with up to eight per output frame, one SHM-copy worker and two reusable GPU staging slots per output. Saturation fails requests instead of waiting or growing a queue. Two 4K slots use about 63 MiB of host-visible memory, capped at 128 MiB per output; HDR adds one GPU-local 8-bit conversion image (about 32 MiB at 4K, up to 64 MiB), outside the host-memory cap. Display capture resources are allocated on first capture and retained until the output stops. Hidden workspace capture uses a separate, demand-driven child renderer and pipeline with its own worker and two staging slots (up to 128 MiB host-visible memory), plus child render targets; each child image is capped at 256 MiB and 16384 pixels per side. The child retires once unused and idle, and is recreated for session or size changes. Copies cost GPU time and bandwidth. Capture of the displayed frame forces composition, and so does the capture indicator (red border or pill, shown while a session lives and for one second after each captured frame; see `docs/headless.md`), which also costs a second composition per capture frame because no capture holds it (a request waits at most 250 ms for the scene that shows its indicator, without drawing anything while it waits); hidden workspace capture can keep display scanout. Offscreen capture currently serves only a whole-workspace target and requests matching its exact physical dimensions, not crops. There is no per-client reservation of the shared request limit.
- **Default GOGC.** The Go live heap is a few MB (client buffers live in shared and GPU memory), so the runtime default `GOGC=100` with a 256 MiB soft limit gives 2 collections on the tiled playback test where `GOGC=50` gave 13. `GOGC` and `GOMEMLIMIT` from the environment win.

Output and input threads request real-time scheduling, and the Vulkan queue requests elevated priority. Both require CAP_SYS_NICE, which the Arch packages set on `/usr/bin/neferwl` at install. Apps launched by NeferWL inherit neither. Set `performance.realtime = false` to opt out. A binary built with `make bin` runs at normal priority unless you grant it: `sudo setcap cap_sys_nice+ep bin/neferwl`.

With the capability, the dynamic loader ignores `LD_PRELOAD` and `LD_LIBRARY_PATH` for NeferWL, and debuggers need root to attach.

## Guards

`make perf-check` runs `testing.AllocsPerRun` guards that fail when a hot path starts allocating: tiled SHM scene walk, tiled commit publication, headless tiled playback, steady-state `Render`, DRM report and flip snapshots, frame lifecycle transitions, scanout/overlay frame decision, pacer bookkeeping, output message routing, surface update recycling and input-region traversal. Run it with `make check`.

## Measuring

Profile a running compositor on another TTY or over SSH.

```sh
# CPU: where the cycles go (Go and C frames).
perf record -g -p (pgrep -f bin/neferwl) -- sleep 8
perf report --children --percent-limit 1

# Allocations: which Go line allocates how many objects.
neferwl --backend=drm --pprof=localhost:6060
go tool pprof -sample_index=alloc_objects -top http://localhost:6060/debug/pprof/heap

# GC: one line per collection (count, heap before/after, CPU).
GODEBUG=gctrace=1 neferwl --backend=drm
```

`make tty` enables it on `localhost:6060` (`make tty PPROF=` disables it). `--pprof` also serves `/debug/pprof/profile` (CPU) and `/debug/pprof/trace` (scheduler and GC timeline for `go tool trace`). Escape analysis (`go build -gcflags=-m ./internal/adapters/vulkan`) shows why a value reaches the heap.

Each output's `stats` entry (every 10 s) counts what happened since the previous one, from the kernel's flip timestamps, and starts over:

- `missed_vblanks`: frame flips that came over 1.5 refresh periods after the previous frame flip. Not counted under VRR, where the period is not fixed.
- `max_flip_interval_ms`: the longest time between two frame flips (also under VRR).
- `max_flip_to_read_ms`: the longest time between a flip's kernel timestamp and the output goroutine handling its event. A large value with few `missed_vblanks` means the flips were on time and the event was read late.
- `late_fences` (only with `--debug=drm-flip`, which reads the fence times): frame flips whose composition fence signalled after the vblank before the flip, so the frame could not have flipped earlier. Not counted under VRR. Many `missed_vblanks` with many `late_fences` is a late composition.
- `redrawn_pixels`: target pixels the renderer drew; a damage-limited frame counts its damage, not the whole target.

`input stats` shows coalesced pointer motion and the longest wait for core.

`--debug=drm-flip` (or `make tty TTY_DEBUG=drm-flip`) logs every commit completion as a `flip` entry: `commit_to_flip_ms`, `flip_interval_ms` (between frame flips), `fence_ready_at_commit`, and, when the client fence reports its signal time, `commit_to_fence_ms` and `fence_to_flip_ms`. A negative `commit_to_fence_ms` means the client finished before the commit.

Logs go to `$XDG_STATE_HOME/neferwl/runs/<backend>/`, one file per run with `latest.log` pointing at the newest; each backend keeps its last 20 runs, so headless test runs never rotate a DRM session's log away. `--debug=all` leaves out the per-event categories `drm-flip`, `input-motion` and `input-keys` (typed text); name them to turn them on.

## Reference: 4K HDR tiled video playback

Workload: a browser playing 4K HDR video with 65 tiled subsurfaces committing every frame, DRM backend, `perf record -g` for 8 s. Percentages are the share of the process's own samples (inclusive).

| Metric | Before (issue #19) | 2026-09-27 |
|---|---|---|
| CPU (top) | 20–30 % | 13–14 % |
| GC (`gcBgMarkWorker`) | 20 % | 8.7 % |
| `runtime.mallocgc` | — | 3.2 % |
| Go → C reflection (`purego.RegisterFunc`) | 46.7 % | 0 |
| C → Go reflection (`reflect.Value.Call`) | ~30 % | 0 |
| Request handlers (`server.dispatch`) | 25.9 % | 27.6 % (real work) |
| Vulkan `Render` | — | 15.3 % |

Component numbers (linux/amd64, Go 1.27, `CGO_ENABLED=0`):

| Path | Before | Now |
|---|---|---|
| C → Go callback, 5 args (purego) | 428 ns, 3 allocs | 101 ns, 0 |
| Go → C, 6 args (purego) | 71 ns, 1 alloc | 34 ns, 0 |
| Wayland request dispatch (purego-libwayland) | 27 ns, 1 alloc | 5 ns, 0 |
| Wayland `PostEvent`, 3 args | 344 ns, 5 allocs | 41 ns, 0 |
| Vulkan Reset+Begin+Barrier+End (purego-vulkan) | 6.4 µs, 22 allocs | 1.2 µs, 0 |
| `Render`, tiled scene, per frame | 1321 allocs | 10 allocs |
| Surface update per commit | 2 allocs | 0 (recycled) |
| DRM report / flip `Shows` per flip | 2 maps | 0 (immutable snapshots) |
| GC on tiled playback test | 13 cycles, 91 ms | 2 cycles, 3 ms |

What remains is compositor work: `surface.Commit` and the fifo logic (~23 %), real C calls (~5 %), the Go scheduler (~8 %).
