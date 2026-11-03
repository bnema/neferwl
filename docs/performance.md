# Performance

NeferWL keeps a Go heap of a few megabytes and uses almost no CPU while the screen does not change.

- Only changed areas are redrawn. Windows nobody sees get one frame callback per second.
- Windows are composed as instanced draws, so a frame makes a few Vulkan calls whatever the number of windows.
- Fullscreen windows go straight to the display (direct scanout), with tearing for games. See `render.direct-scanout` and `render.tearing` in [Configuration](config.md#keys).
- A full frame at 2560×1600 takes about 0.16 ms on an RX 9070 XT and 3 ms on a Radeon 680M. Measure yours with `go test ./internal/adapters/vulkan -bench ComposeFullTarget` (`NEFERWL_RENDER_DEVICE=/dev/dri/renderDN` picks the GPU).
- An output on a non-render GPU costs nothing extra in direct mode and one GPU blit per frame in gpu-copy mode. See [GPUs](config.md#gpus).
- NeferWL runs with `GOGC=100` and a 256 MiB soft memory limit. `GOGC` and `GOMEMLIMIT` from the environment override them.

## Memory

Measured on a headless NeferWL with an empty config: one 1920×1080 output, no client, no Xwayland, AMD GPU (RADV), one minute after start.

| Part | Size | Source |
|---|---|---|
| Resident memory (RSS) | 56 MB | `VmRSS` |
| Mapped files | 26 MB | `RssFile` |
| ⤷ LLVM, loaded by the Mesa Vulkan driver | 15 MB | `smaps` |
| ⤷ NeferWL binary | 14 MB | `smaps` |
| ⤷ Vulkan driver (`libvulkan_radeon`) | 5 MB | `smaps` |
| Shared memory: output images | 13 MB | `RssShmem` |
| Private memory | 17 MB | `RssAnon` |
| Go runtime, all of it | 18 MB | pprof `Sys` |
| ⤷ Go heap in use | 3.5 MB | pprof `HeapInuse` |
| ⤷ Live Go objects | 1.7 MB | pprof `HeapAlloc` |
| ⤷ Goroutine stacks (42 goroutines) | 1.1 MB | pprof `Stack` |

The driver libraries are shared with other Vulkan programs and their size depends on the GPU and driver.

On the GPU, the same setup uses about 36 MB of VRAM and 4 MB of GTT (system memory mapped for the GPU), read from the `drm-memory-vram` and `drm-memory-gtt` lines of `/proc/<pid>/fdinfo`. A DRM session adds scanout buffers for each output.

This is the cost of NeferWL alone. On the normal pread path, NeferWL reads wl_shm client pixels without mapping client pools, so shm clients do not add to its RSS; their copies live in GPU memory (fdinfo `drm-memory-vram`/`drm-memory-gtt`). On drivers that reject kernel copies into GPU mappings, NeferWL uses a temporary client pool mapping and releases it right after the copy. Every extra output adds its own images. A desktop with two 4K screens, a wallpaper and a bar uses about 100 MB.

To measure your own session, start NeferWL with `--pprof localhost:6060`, then:

```sh
curl -s 'localhost:6060/debug/pprof/heap?debug=1' | grep -E '^# (Sys|HeapAlloc|HeapInuse|HeapIdle|HeapReleased|Stack) '
grep -E '^(VmRSS|RssAnon|RssFile|RssShmem)' /proc/$(pgrep -x neferwl)/status
```

The split by library needs root when NeferWL runs with extra capabilities (the packages set `CAP_SYS_NICE`): `sudo cat /proc/$(pgrep -x neferwl)/smaps`.

## VRR

With a fullscreen game under VRR, each game frame is committed at least `render.vrr-flip-gap` (default 1 ms) after the previous flip. On amdgpu, committing earlier can hold the panel at its slowest refresh rate.

## Real-time priority

Output and input threads ask for real-time scheduling, which needs `CAP_SYS_NICE`. The Arch and Debian packages set it. For a local build:

```sh
sudo setcap cap_sys_nice+ep bin/neferwl
```

`performance.realtime = off` turns it off. With the capability set, the loader ignores `LD_PRELOAD` and `LD_LIBRARY_PATH`, and debuggers need root to attach.

For profiling tools and frame statistics, see [Profiling](dev/profiling.md).
