<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue?style=flat-square" alt="License: GPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://img.shields.io/github/last-commit/bnema/neferwl/main?style=flat-square&logo=github" alt="Last commit"></a>
  <a href="https://github.com/bnema/neferwl/stargazers"><img src="https://img.shields.io/github/stars/bnema/neferwl?style=flat-square&logo=github" alt="GitHub stars"></a>
</p>

<p align="center">A Wayland compositor that spends its frames on your apps, not on itself.</p>

> [!WARNING]
> **Early alpha, but stable enough for daily use.** NeferWL is developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested; expect bugs and breaking config changes.

---

## Why NeferWL

I wanted a compositor I would never have to think about. It takes the newest Wayland protocols and kernel features as they land, gives games the whole GPU, and otherwise stays out of sight.

It is small on purpose, and it will stay small. Drawing is kept to simple primitives: no blur, no shadows, no rounded corners, no themes. There is no built-in bar or wallpaper either; for those, bring your own: any layer-shell client works. Windows and the view move with short springs (about 300 ms) when you scroll, switch workspaces, move columns or windows, resize them, drop a dragged tile or send a workspace to another monitor; windows fade in and out when they open, close or leave with the stash, the overview zooms its cards, and a brief pulse marks the window you just focused. They stay cheap: nothing runs while the screen is still, frames follow the display, only the region that moves is redrawn, apps are resized once, to their final size, and do not redraw during a transition, and nothing is copied or blurred. `animations = off` makes every change instant. About 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change.

## OK, but why Go?

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
- **The GC has almost nothing to collect.** Client buffers live in shared and GPU memory, not on the Go heap, so the live heap is a few megabytes. Playing a 4K HDR video with 65 tiled subsurfaces committing every frame, GC takes under 9 % of a process that itself takes 13 % of a core. See [Performance](docs/performance.md).
- **No cgo.** NeferWL builds with `CGO_ENABLED=0`. libwayland, Vulkan and libinput are loaded at runtime through [purego](https://github.com/ebitengine/purego) with fixed-arity calls: no reflection, no allocation at the boundary.
- **Work only when something changes.** Damage tracking redraws only what changed, surfaces hidden behind opaque ones are skipped, and an idle output does nothing.
- **Tuned on real hardware.** Under VRR, cursor moves ride on the game's next frame instead of forcing their own refresh, and game frames keep a short gap after each flip so the panel does not fall back to its slowest rate.

### A pleasure to work on

A full build takes seconds, and tests, race detection, profiling and formatting come with the language. I also like bringing more tools to the Go ecosystem. NeferWL grew its own libraries along the way, and other Go projects can use them:

- [purego-libwayland](https://github.com/bnema/purego-libwayland): libwayland-server without cgo.
- [purego-vulkan](https://github.com/bnema/purego-vulkan): Vulkan without cgo, generated from `vk.xml`.
- [go-wayland-bindings](https://github.com/bnema/go-wayland-bindings): Wayland protocol bindings, generated from the upstream XML.
- [wlturbo](https://github.com/bnema/wlturbo): a fast Wayland client.
- [neferclient](https://github.com/bnema/neferclient): a client toolkit on top of them: connection, outputs, surface roles, seat and dmabuf presentation.

## Features

- **Games.** A fullscreen window is scanned out directly: its buffer goes to the display without a composition pass. Wine runs natively on Wayland; Steam and other X11 clients run through xwayland-satellite. See [the performance path](#the-performance-path).
- **VRR.** Variable refresh rate turns on while a fullscreen window covers the output, so the display follows the game's frame rate. On by default (`render.vrr`).
- **Screen tearing.** A game in direct scanout that asks for tearing gets it, for the lowest input latency. On by default (`render.tearing`).
- **Explicit sync.** On by default for every client, with no flag to set. Client fences go straight to the GPU and to KMS, so frames are shown when they are ready, without implicit-sync stalls.
- **HDR.** HDR10 output on capable displays. HDR clients (games, browsers, video players) are shown at full range; SDR content is shown at a configured brightness.
- **Column tiling.** Columns scroll to the right, as in PaperWM and Niri, or stay on screen and split, as in Sway. Per output or per workspace.
- **Overview.** `cmd+o` by default (or a four-finger swipe up) shows the workspaces of a monitor as live, scaled previews. In `fixed` mode, windows hidden behind a maximized column show as a stack of cards behind it, so nothing is lost out of sight. Pick a window with the keyboard, the touchpad or a click.
- **Stash.** Set aside a window you do not need right now. Each workspace has its own stash, with no limit on the number of windows. By default, `cmd+s` (or a four-finger swipe down) shows it as a floating strip over your tiles, or hides it again (a four-finger swipe up hides it too, while it is shown); `cmd+shift+s` sends a window into the stash or back to its column.
- **Workspaces.** Numbered workspaces are created and removed as needed. Named workspaces declare their columns (width and command); NeferWL starts the commands and places each window.
- **Multi-monitor.** Each output has its own workspaces. When an output is unplugged its workspaces move to another one, and return when it is plugged back. Fractional scale per output.
- **External clients.** No built-in bar, launcher or notifications. Waybar, fuzzel, mako, nefercap, cliphist, swayidle, wlr-randr and input methods such as fcitx5 work through their standard protocols. See [Desktop integration](docs/desktop.md).
- **Simple config.** One flat `key = value` file, reloaded on save without closing windows. A bad line only warns and keeps its default, and `neferwl validate-config` checks a file before you use it. A JSON state file describes outputs, workspaces and windows for scripts.

Config example:

```text
keyboard.layout = fr
terminal.auto-open = first
startup = waybar

output.DP-1 = 3840x2160@144
output.DP-1.scale = 1.5

layout.max-columns = 3

# A bind-only dev workspace: editor and terminal, started and placed for you.
workspace.dev.monitor = DP-1
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 33%, foot
bind.cmd+d = workspace dev
```

The terminal command comes from `terminal`, then `$TERMINAL`, then `foot`. `terminal.auto-open = first` opens it once on the initial numbered workspace; `all` opens it on each visible empty workspace without slots, and `off` disables automatic opening. `--no-terminal` overrides automatic opening for one run; `spawn-terminal` still works. Launched programs inherit `$SHELL`, `$EDITOR`, and `$VISUAL`.

## The performance path

- `zwp_linux_dmabuf_v1` v6: client buffers are sampled in place. Vulkan composes into exported scanout images and the frame fence goes to KMS as `IN_FENCE_FD`. No CPU copy, no CPU wait.
- `wp_single_pixel_buffer_v1`: solid backgrounds and letterbox bars are drawn as fills, without client buffers.
- Direct scanout of a fullscreen window's buffer; dmabuf feedback gives it a scanout-ready format first. A lone opaque window can use an overlay plane. On HDR outputs, SDR windows also bypass composition when the plane has a colour pipeline.
- `wp_tearing_control_v1` and VRR while a fullscreen window covers the output.
- `wp_linux_drm_syncobj_v1`: client fences go to KMS.
- `wp_presentation`, `wp_fifo_v1`, `wp_commit_timing_v1` for frame pacing.
- `wp_fractional_scale_v1` and `wp_viewporter`: clients render at the output scale, nothing is rescaled.
- Damage tracking, occlusion of surfaces behind opaque ones, no work on idle outputs. Frame, cursor, overlay and VRR in one atomic commit.
- Input is read on its own thread and never waits on rendering; the cursor has a hardware plane. With CAP_SYS_NICE, input and output threads request real-time scheduling. Relative pointer, pointer constraints and keyboard shortcuts inhibit for games. `zwp_input_timestamps_v1` gives clients microsecond input timestamps from libinput.
- `wp_drm_lease_v1` leases headset connectors to SteamVR and Monado.

## Hardware support

NeferWL runs on Linux. It needs a GPU and kernel driver with atomic KMS, and a Vulkan 1.3 device that can export images as dmabufs. Without them, it stops at startup with an error.

- **AMD:** developed and tested on radv.
- **NVIDIA:** untested. Explicit sync, which NVIDIA drivers need, is implemented. Reports from NVIDIA users are welcome.
- **Intel:** untested.

## Install (Arch Linux)

From the AUR:

```sh
paru -S neferwl-bin   # latest release, pre-built for x86_64 and aarch64
paru -S neferwl-git   # latest main, built from source
```

From a checkout (needs Go 1.27):

```sh
make install   # builds dist/neferwl-*.pkg.tar.zst from HEAD, then installs it with pacman
```

`make pkg` only builds the package. The package installs `neferwl` with CAP_SYS_NICE (real-time scheduling, see [performance](docs/performance.md)) and a **NeferWL** session for display managers such as Ly, GDM and SDDM. The session runs `neferwl-session`, which starts `neferwl.service` under systemd. This starts `graphical-session.target` and services bound to it (bars, notification daemons, portals, and XDG autostart) with NeferWL and stops them with it. Running `neferwl --session` alone still exports the display environment to D-Bus and systemd user services.

On other distributions, download a `linux_amd64` or `linux_arm64` archive from [Releases](https://github.com/bnema/neferwl/releases) and check it against `checksums.txt`, or run `make bin` to build `bin/neferwl`.

## Configuration

NeferWL reads `$XDG_CONFIG_HOME/neferwl/config`, or `~/.config/neferwl/config`. `neferwl --config <path>` selects another file, and `neferwl validate-config [path]` checks a file without starting the compositor.

The file has one `key = value` per line, and `#` starts a comment. A missing file or key means the default. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Changes apply when you save, and each reload logs the keys that changed.

[docs/config.md](docs/config.md) lists every key, action and default bind. `examples/config` is a commented copy of the defaults.

## Documentation

- [Configuration](docs/config.md): every key, action and default bind, and HDR.
- [Desktop integration](docs/desktop.md): running commands, clipboard, input methods, X11 apps, idle and screen off, bars, and the state file for scripts.
- [Session locking](docs/session-lock.md): external lockers, output confirmation, owner recovery and security limits.
- [Screen capture](docs/capture.md): capture protocols, the indicator, the executable allowlist, sandboxed clients and portals.
- [Headless mode](docs/headless.md): run without a screen, take screenshots, play input scripts and test input methods.
- [Performance](docs/performance.md): how to profile, the allocation guards, and reference numbers.

## Developing the bindings

NeferWL uses tagged releases of [purego-libwayland](https://github.com/bnema/purego-libwayland) and [purego-vulkan](https://github.com/bnema/purego-vulkan). To change a binding and NeferWL together, clone the binding next to NeferWL and use a Go workspace, which is not committed:

```sh
go work init . ../purego-libwayland ../purego-vulkan
```

`make pkg` ignores the workspace and builds the versions in `go.mod`. Tag the binding change and update `go.mod` before packaging.

## Contributing

Contributions are welcome. Open an issue first, for a bug fix or a feature, and wait until it is accepted before you send a pull request. Pull requests without an accepted issue are closed.

Before you open a pull request, `make check`, `make race`, `make mocks-check` and `staticcheck ./...` must pass. Repository rules are in [AGENTS.md](AGENTS.md).

## License

NeferWL is licensed under the [GNU General Public License v3.0](LICENSE).
