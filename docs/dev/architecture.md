# Architecture

Most Wayland compositors are written in C (wlroots, Sway, Mutter), C++ (KWin, Hyprland, gamescope) or Rust (niri, COSMIC). The usual objection to Go is the garbage collector. It is a fair one, so here is how NeferWL deals with it, and why Go turns out to fit a compositor well.

### A compositor is a concurrency problem

A compositor sits in the middle of many independent streams that never stop: keyboard and pointer events, client requests and buffer commits, one vblank and page-flip cycle per monitor, frame fences from the GPU, presentation feedback, screen captures, config reloads, hotplug, idle and lock state. None of them should wait on another. A slow client must not delay a flip, and rendering must not delay input.

Go has this built in. Goroutines are cheap, channels carry messages between them, and the runtime spreads them over every CPU core. NeferWL follows one idiom everywhere: **each piece of state has one owner goroutine, and others talk to it over channels.**

- **Input** runs on its own thread, reads libinput and sends events to core.
- **Core** owns windows, workspaces and focus. It turns input and client events into layouts and scenes.
- **The Wayland server** owns client connections. It sends client events and surface content, and receives commands back.
- **Each output** owns its DRM state, renders its scenes with Vulkan, commits frames and reports flips and presentation times.
- **Capture, config, idle, sessions and leases** each run in their own goroutine.

About 35 channels connect them, all created in one place, so the full data flow is easy to read. There is no mutex on window state, no lock ordering to get wrong, and the race detector checks every test. When CAP_SYS_NICE is granted, the input and output goroutines lock their OS thread and request real-time scheduling, so a busy CPU cannot delay a cursor move or a flip.

### Keeping the garbage collector out of the way

- **The hot path does not allocate.** Frame callbacks, presentation reports, DRM flips, Vulkan submissions and surface updates reuse memory owned by the goroutine that handles them. `make check` runs allocation guards that fail the build when one of these paths starts allocating.
- **The GC has almost nothing to collect.** Client buffers live in shared and GPU memory, not on the Go heap, so the live heap is a few megabytes. Playing a 4K HDR video with 65 tiled subsurfaces committing every frame, GC takes under 9 % of a process that itself takes 13 % of a core. See [Performance](../performance.md).
- **No cgo.** NeferWL builds with `CGO_ENABLED=0`. libwayland, Vulkan, libinput and GBM are loaded at runtime through [purego](https://github.com/ebitengine/purego) with fixed-arity calls: no reflection, no allocation at the boundary.
- **Work only when something changes.** Damage tracking redraws only what changed, surfaces hidden behind opaque ones are skipped, and an idle output does nothing.
- **Tuned on real hardware.** Under VRR, cursor moves ride on the game's next frame instead of forcing their own refresh, and game frames keep a short gap after each flip so the panel does not fall back to its slowest rate.

### Output images

A DRM output flips between two images the renderer draws into, with no CPU copy.

- **Allocation:** the images are allocated with GBM (`GBM_BO_USE_SCANOUT | GBM_BO_USE_RENDERING`), with the modifiers the primary plane lists that the renderer can render into and import. The renderer imports them as Vulkan images, and KMS gets framebuffers made from the same dmabufs. GBM is the display driver's own allocator, so the layouts it picks scan out correctly on every driver.
- **Fallback:** when libgbm is missing, GBM allocates nothing usable, or Vulkan or KMS refuses the images, the renderer allocates and exports them itself. Each kind (driver modifier, single-plane, linear) tries GBM, then the renderer. The log line `zero-copy output` names the allocator in its `alloc` field (`gbm` or `vulkan`), and is a warning for `vulkan`. This fallback is deprecated and goes away once GBM is validated on more setups (neferwl#23).
- **Sync:** nvidia-drm before 560.35.3 does not wait for `IN_FENCE_FD`. On it, the output waits for each frame's fence on the CPU and commits without one. A client buffer whose fence is still pending is composed, not scanned out, while `IN_FENCE_FD` is unavailable.

### Cursor images

The cursor plane shows the hardware cursor from two ARGB8888 images that the CPU writes only when the cursor image changes, never on a move.

- **Allocation:** the images are KMS dumb buffers, mapped for the CPU, with the pitch the driver picks. The output converts them to HDR10 when it is in HDR.
- **Fallback:** when KMS cannot make dumb buffers, or the plane refuses them in a TEST_ONLY commit, the renderer's linear cursor images are used. When the plane refuses those too, the cursor is drawn into every frame (software cursor). The log line `hardware cursor` names the allocator in its `alloc` field (`dumb` or `vulkan`), and is a warning for `vulkan`.

I also like bringing more tools to the Go ecosystem. NeferWL grew its own libraries along the way, and other Go projects can use them:

- [purego-libwayland](https://github.com/bnema/purego-libwayland): libwayland-server without cgo.
- [purego-vulkan](https://github.com/bnema/purego-vulkan): Vulkan without cgo, generated from `vk.xml`.
- [go-wayland-bindings](https://github.com/bnema/go-wayland-bindings): Wayland protocol bindings, generated from the upstream XML.
- [wlturbo](https://github.com/bnema/wlturbo): a fast Wayland client.
- [neferclient](https://github.com/bnema/neferclient): a client toolkit on top of them: connection, outputs, surface roles, seat and dmabuf presentation.

