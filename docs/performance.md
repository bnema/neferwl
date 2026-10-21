# Performance

NeferWL uses about 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change.

- Only changed areas are redrawn. Windows nobody sees get one frame callback per second.
- Windows are composed as instanced draws, so a frame makes a few Vulkan calls whatever the number of windows.
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

For profiling tools and frame statistics, see [Profiling](dev/profiling.md).
