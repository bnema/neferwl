# Performance

How NeferWL keeps its CPU and memory use low, how to measure it, and the reference numbers to compare against.

## Design

- **No reflection on the FFI boundary.** libwayland and Vulkan are called through `purego.Syscall6/Syscall15` (fixed arity, zero allocation) and C calls back into Go through `purego.NewCallbackInts`. The Vulkan dispatch tables are generated methods; the libwayland wrapper caches resource id, version and client, and precomputes request argument counts.
- **Allocation-free steady-state presentation.** Frame callbacks, presentation reports, DRM flips, Vulkan submissions and surface updates reuse owner-goroutine scratch or immutable snapshots. Surface updates (one per `wl_surface.commit`) are recycled once no queued update references them.
- **Cursor rides on game frames under VRR.** While a fullscreen game drives VRR, a cursor move waits for the game's next frame instead of its own commit: a cursor-only commit refreshes the panel at its slowest rate (~21 ms at 48 Hz) and delays the next game frame by as much. If the game stops drawing, the cursor still commits alone at 24 Hz.
- **Game frames keep a short gap after each VRR flip.** On amdgpu, a flip committed right after the previous flip event can miss the early refresh and wait for the panel's slowest rate (~21 ms at 48 Hz); the next frame then lands at the same point, and the screen stays at 48 Hz while the game renders at 150 fps. Game frames under VRR therefore commit at least `render.vrr-flip-gap` (default 1 ms) after the previous flip. `internal/adapters/drm/vrr_flip_gap.go` has the measurements.
- **Transitions** (experimental, `animations = on`; off by default). Swipe landings, scrolling, workspace switches, moving columns and windows, consume and expel, column width and window height, maximize, fullscreen, the floating toggle, drag drops and workspaces sent to another monitor each move by one critically damped spring per moving value, advanced on the flips of the output that shows them. Windows that open, close or leave with the stash fade and scale from 90 %, and the overview zooms its cards; both reuse the per-window alpha and preview zoom of the compose pass, so no transition adds a GPU pass, a blur or a texture copy. A closing window is drawn from its last content, which the renderer already imported; the veil under a closing float goes at once, so the close redraws only the float's rect. A window that fades or zooms is never scanned out directly. Client-driven resizes, panels and hotplug never animate. Clients get one configure, to the final size. During a move or a resize the window frame moves and the content is not scaled, only clipped or padded; the content zooms with the frame only while a window opens, closes or is an overview card. A spring that moves pixels settles as soon as it can no longer leave half a pixel of its target, so its last rendered frame is already the settled one and the sub-pixel tail (about a third of a 100 px move's frames) is not drawn; fades keep their fine epsilon. The frame fallback timer is made once and reset per frame.
- **Redraw only what moved.** An output whose scene is unchanged keeps its scene `Seq` and is not recomposed; a content-only change redraws only the damaged region. When only windows move, the renderer compares the new scene with the one each render target holds and redraws the union of the old and new rects of the windows that changed; anything else (layers, scale, transform, dim, capture) redraws the whole output. Resizing one of three columns at 2560×1600 redraws about 0.2 M pixels per frame instead of 4.1 M. Advancing the springs, the scene comparison (separator lists of the same length are compared index by index, as core emits them in layout order) and the draw list of a moving window cost 0 allocations; publishing the scenes of two outputs with six moving windows (position, fade, dim and zoom motions) costs a pinned 6 on both the page-flip and the fallback-timer paths, 4 with closing windows and 18 while the overview opens. All are guarded by `make perf-check`. Measured cost of a full composed frame at 2560×1600: ~0.16 ms on an RX 9070 XT, ~1.4 ms on a Raphael iGPU, ~3 ms on a Radeon 680M, against an 11.1 ms frame at 90 Hz.
- **Compressed render targets.** Output images take their modifiers from the primary plane's `IN_FORMATS` for XRGB8888 (XRGB2101010 in HDR); KMS refuses any other (`ADDFB2: EINVAL`). A multi-plane (AMD DCC) modifier is used only when the plane lists the exact variant the GPU exports; otherwise NeferWL uses a single-plane tiled modifier, then linear. AMD DCC retile modifiers (`DCC_RETILE`, bit 14 of the modifier, e.g. `…567b03`) are never exported: such an image is rendered into pipe-aligned DCC and a compute pass rewrites its displayable copy, a full-surface GPU pass on top of the composition. On the measured Radeon 680M laptop (RDNA2, current RADV and amdgpu) the pick the plane accepts is single-plane tiled `0x200000010401b03`. Plain DCC costs no retile pass. As a renderer-only benchmark on a Raphael iGPU, a full composed frame at 2560×1600 takes about 1.63 ms into pipe-aligned DCC (`…56bb03`), 1.85 ms into single-plane tiled (`…401b03`) and 3.0 ms into linear (`go test ./internal/adapters/vulkan -bench ComposeFullTarget`, `NEFERWL_VK_DEVICE` picks the GPU; it exports one target per modifier the device offers, directly and not through the plane's `IN_FORMATS`, and times one frame with the GPU wait included). The DCC figure is thus not what the DRM output gets unless the plane lists that modifier. The chosen modifier and its plane count are logged as `zero-copy output`.
- **Bounded capture.** Protocol requests and sync-file handles still need small allocations; pixel storage and worker batches are reused. At most 32 protocol requests are outstanding globally, with up to eight per output frame, one SHM-copy worker and two reusable GPU staging slots per output. Saturation fails requests instead of waiting or growing a queue. Two 4K slots use about 63 MiB of host-visible memory, capped at 128 MiB per output; HDR adds one GPU-local 8-bit conversion image (about 32 MiB at 4K, up to 64 MiB), outside the host-memory cap. Display capture resources are allocated on first capture and retained until the output stops. Hidden workspace capture uses a separate, demand-driven child renderer and pipeline with its own worker and two staging slots (up to 128 MiB host-visible memory), plus child render targets; each child image is capped at 256 MiB and 16384 pixels per side. The child retires once unused and idle, and is recreated for session or size changes. Copies cost GPU time and bandwidth. Capture of the displayed frame forces composition, and so does the capture indicator (red border or pill, shown while a session lives and for one second after each captured frame; see `docs/headless.md`), which also costs a second composition per capture frame because no capture holds it (a request waits at most 250 ms for the scene that shows its indicator, without drawing anything while it waits); hidden workspace capture can keep display scanout. Offscreen capture currently serves only a whole-workspace target and requests matching its exact physical dimensions, not crops. There is no per-client reservation of the shared request limit.
- **Default GOGC.** The Go live heap is a few MB (client buffers live in shared and GPU memory), so the runtime default `GOGC=100` with a 256 MiB soft limit gives 2 collections on the tiled playback test where `GOGC=50` gave 13. `GOGC` and `GOMEMLIMIT` from the environment win.

Output and input threads request real-time scheduling, and the Vulkan queue requests elevated priority. Both require CAP_SYS_NICE, which the Arch packages set on `/usr/bin/neferwl` at install. Apps launched by NeferWL inherit neither. Set `performance.realtime = false` to opt out. A binary built with `make bin` runs at normal priority unless you grant it: `sudo setcap cap_sys_nice+ep bin/neferwl`.

With the capability, the dynamic loader ignores `LD_PRELOAD` and `LD_LIBRARY_PATH` for NeferWL, and debuggers need root to attach.

## Guards

`make perf-check` runs `testing.AllocsPerRun` guards that fail when a hot path starts allocating: spring advance, scene publication (pinned budgets, with the overview open and with closing windows), scene comparison for partial redraws, tiled SHM scene walk, tiled commit publication, headless tiled playback, steady-state `Render`, DRM report and flip snapshots, frame lifecycle transitions, scanout/overlay frame decision, DRM frame commit and plane colour pipeline decision, pacer bookkeeping, output message routing, surface update recycling and input-region traversal. Run it with `make check`.

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

- `missed_vblanks`: frame flips that came over 1.5 refresh periods after the previous frame flip. An output renders only on change, so only a frame that was due counts: one wanted (a scene, content or capture change made it dirty) before, or less than one period after, the previous flip, so it was meant for the next vblank even if it was committed late. A frame wanted after an idle period is ignored. Not counted under VRR, where the period is not fixed.
- `max_flip_interval_ms`: the longest interval between two flips of due frames (also under VRR, where the gate uses the mode's refresh period, the shortest one). The chain restarts after a modeset or a VRR change.
- `max_commit_delay_ms`: for due frames, the longest time from when the frame was due (wanted, and not before the previous flip or the end of the deliberate `render.vrr-flip-gap`) to its commit. It is large when the commit came late: the previous flip's event was read late, or the CPU side of the composition was slow.
- `max_flip_to_read_ms`: the longest time between a flip's kernel timestamp and the output goroutine handling its event, for the output's own commits.
- `late_fences` (only with `--debug=drm-flip`, which reads the fence times): missed vblanks whose composition fence signalled after the vblank the frame targeted (the previous flip plus one period), so the GPU was not done in time. Not counted under VRR.
- `redrawn_pixels`: target pixels the renderer drew; a damage-limited frame counts its damage, not the whole target.

Reading them together: missed vblanks with `late_fences` are a late GPU composition. Missed vblanks without late fences and with a large `max_commit_delay_ms` (usually next to a large `max_flip_to_read_ms`) are a late commit: the event reader or the CPU. Few `missed_vblanks` mean flips were on time only if `max_commit_delay_ms` is also small: a delayed commit can still make its vblank, and `max_flip_to_read_ms` alone says the event arrived late, not that the flip did.

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
