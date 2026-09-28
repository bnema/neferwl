# Performance

How NeferWL keeps its CPU and memory use low, how to measure it, and the reference numbers to compare against.

## Design

- **No reflection on the FFI boundary.** libwayland and Vulkan are called through `purego.Syscall6/Syscall15` (fixed arity, zero allocation) and C calls back into Go through `purego.NewCallbackInts`. The Vulkan dispatch tables are generated methods; the libwayland wrapper caches resource id, version and client, and precomputes request argument counts.
- **No allocation per frame or per request on the steady-state path.** Frame callbacks, presentation reports, DRM flips, Vulkan submissions and surface updates reuse owner-goroutine scratch or immutable snapshots. Surface updates (one per `wl_surface.commit`) are recycled once no queued update references them.
- **Default GOGC.** The Go live heap is a few MB (client buffers live in shared and GPU memory), so the runtime default `GOGC=100` with a 256 MiB soft limit gives 2 collections on the tiled playback test where `GOGC=50` gave 13. `GOGC` and `GOMEMLIMIT` from the environment win.

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

`--pprof` also serves `/debug/pprof/profile` (CPU) and `/debug/pprof/trace` (scheduler and GC timeline for `go tool trace`). Escape analysis (`go build -gcflags=-m ./internal/adapters/vulkan`) shows why a value reaches the heap.

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
