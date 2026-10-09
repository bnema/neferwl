<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue?style=flat-square" alt="License: GPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://img.shields.io/github/last-commit/bnema/neferwl/main?style=flat-square&logo=github" alt="Last commit"></a>
  <a href="https://github.com/bnema/neferwl/stargazers"><img src="https://img.shields.io/github/stars/bnema/neferwl?style=flat-square&logo=github" alt="GitHub stars"></a>
</p>

<p align="center">A Wayland compositor that spends its frames on your apps, not on itself.</p>

> [!WARNING]
> **Early alpha, but I use it every day.** Developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested. Expect bugs and breaking config changes.

## Why NeferWL

I wanted a compositor I would never have to think about. It takes the newest Wayland protocols and kernel features as they land, gives games the whole GPU, and otherwise stays out of sight.

It is small on purpose, and it will stay small. No blur, no shadows, no rounded corners, no themes. No built-in bar or wallpaper either: bring your own, any layer-shell client works.

Animations are short, cheap and off by default (experimental, `animations = on`).

About 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change. See [Performance](docs/performance.md).

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

### Display and games

- **Direct scanout:** eligible fullscreen buffers go straight to the display, without composition.
- **VRR and tearing:** enabled by default for supported fullscreen use (`render.vrr`, `render.tearing`). Tearing requires direct scanout and a client request.
- **Explicit sync:** client fences go to the GPU and KMS. Enabled by default, with no flag to set.
- **HDR:** HDR10 on capable displays, with configurable brightness for SDR content.
- **Multi-monitor:** separate workspaces and fractional scale per output. Workspaces move when a monitor is unplugged and return when it reconnects.
- **Hidden apps rest:** a window you cannot see (another workspace, a hidden stash, scrolled off screen, under a fullscreen window) gets frame callbacks once per second and the suspended state. Browsers and games stop drawing frames nobody sees. A window being captured keeps its output's refresh rate.
- **X11 apps:** Steam and other X11 clients run through xwayland-satellite. Wine can run natively on Wayland.

### Windows and workspaces

- **Cascade tiling (default):** an original layout. Columns fill the screen side by side, then continue in a new band below; scroll bands vertically and switch workspaces sideways. See [Cascade](docs/config.md#cascade).
- **Other layouts:** scroll horizontally, as in PaperWM and niri, or keep columns on screen in a fixed spiral. Choose per output or workspace.
- **Overview:** `cmd+o` or a four-finger swipe up shows live workspace previews. Select a window with the keyboard, touchpad or mouse.
- **Stash:** `cmd+s` toggles a floating strip of windows set aside for the current workspace. `cmd+shift+s` moves a window into or out of it.
- **Workspaces:** numbered workspaces appear as needed. Named workspaces can start commands and place their windows in predefined columns.

### Desktop integration

Bring your own desktop UI: any layer-shell client, such as Noctalia or Waybar, works. No built-in bar, launcher or notifications; input methods like fcitx5 work through their standard protocols. For capture and idle management, see [nefercap](https://github.com/bnema/nefercap) and [neferafk](https://github.com/bnema/neferafk).

Configuration is a flat `key = value` file that reloads on save. A JSON state file exposes outputs, workspaces and windows to scripts. See [Desktop integration](docs/desktop.md).

## Install

### Arch Linux

Choose an AUR package:

```sh
paru -S neferwl-bin   # latest release, pre-built for x86_64 and aarch64
paru -S neferwl-git   # latest main, built from source
```

Or install from a checkout with Go 1.27:

```sh
make install   # build a package from HEAD and install it with pacman
```

`make pkg` builds the package without installing it.

### Ubuntu 24.04 and later

Download the `.deb` for your architecture (`amd64` or `arm64`) and `checksums.txt` from [Releases](https://github.com/bnema/neferwl/releases), then install it with apt, which pulls the runtime dependencies:

```sh
sha256sum -c --ignore-missing checksums.txt
sudo apt install ./neferwl_<version>_amd64.deb
```

Optional tools:

```sh
sudo apt install foot fuzzel xdg-desktop-portal-gtk xdg-desktop-portal-wlr
```

X11 apps need [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, which you may have to build. The package is tested on Ubuntu 24.04 and 26.04; Debian with the same library packages should work but is untested.

### What the packages provide

The Arch and Debian packages provide:

- `neferwl` with CAP_SYS_NICE for real-time scheduling.
- A **NeferWL** session for display managers such as Ly, GDM and SDDM.
- `neferwl-session`, which starts `neferwl.service` and manages `graphical-session.target` so desktop services start and stop with the session.

Running `neferwl --session` directly also exports the display environment to D-Bus and systemd user services. See [Desktop integration](docs/desktop.md) for session setup.

Copy `/usr/share/doc/neferwl/config.example` to `~/.config/neferwl/config` to start from the example config.

### Other distributions

Download a `linux_amd64` or `linux_arm64` archive from [Releases](https://github.com/bnema/neferwl/releases) and verify it against `checksums.txt`. To build locally, run `make bin`; the binary is `bin/neferwl`.

## Hardware support

NeferWL requires Linux, atomic KMS and a Vulkan 1.3 device that can export images as dmabufs. It reports an error at startup if these requirements are not met.

| GPU | Status |
| --- | --- |
| AMD | Developed and tested on radv |
| NVIDIA | Untested; explicit sync is implemented, but support is incomplete |
| Intel | Untested |

Reports from NVIDIA and Intel users are welcome.

## Configuration

The default file is `$XDG_CONFIG_HOME/neferwl/config`, or `~/.config/neferwl/config`.

```text
keyboard.layout = fr
terminal.auto-open = first
startup = waybar

output.DP-1 = 3840x2160@144
output.DP-1.scale = 1.5

layout.max-columns = 3

# Start and place an editor and terminal when this workspace is opened.
workspace.dev.monitor = DP-1
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 33%, foot
bind.cmd+d = workspace dev
```

See [Configuration](docs/config.md) for all keys, actions and default bindings, or [examples/config](examples/config) for commented defaults.

- Use one `key = value` per line; `#` starts a comment.
- Missing keys use defaults. Invalid lines warn and keep that key's default; other lines still apply.
- Save to reload. The log lists changed keys.
- Use `neferwl --config <path>` to select a file, or `neferwl validate-config [path]` to check it without starting the compositor.

### Default configuration

<details>
<summary>All default settings and key bindings</summary>

These values apply without a config file. Empty values use the system setting; `terminal` uses `$TERMINAL`, then `foot`. No startup commands or named workspaces are configured by default. `cmd` means Super.

```text
# Keyboard and commands
keyboard.layout =
keyboard.variant =
keyboard.options =
keyboard.repeat-rate = 25
keyboard.repeat-delay = 600
keyboard.cmd = super
terminal =
terminal.auto-open = first
xwayland = xwayland-satellite

# Appearance and layout
background = #111111
border.width = 2
border.active = #808080
border.inactive = #111111
floating.dim = 0.3
layout.gaps = 0
layout.max-columns = 2
layout.overflow = cascade
layout.presets = 1/3, 1/2, 2/3, 1
stash.width = 80
stash.gap = 2
stash.dim = 0.5
stash.capture = on

# Input
touchpad.natural-scroll = off
touchpad.tap = on
touchpad.accel-speed = 0
touchpad.accel-profile = adaptive
touchpad.left-handed = off
touchpad.scroll-factor = 1
mouse.natural-scroll = off
mouse.accel-speed = 0
mouse.accel-profile = adaptive
mouse.left-handed = off
cursor.hide-after = 5s

# Focus and animations
focus.follow-move = off
animations = off
animations.speed = normal
focus.animation = pulse
focus.effect = screen
focus.strength = 0.04

# Rendering and logging
render.direct-scanout = on
render.tearing = on
render.vrr = on
render.vrr-flip-gap = 1ms
performance.realtime = on
log.level = info
log.debug =

# Launch, close and session
bind.cmd+return = spawn-terminal
bind.ctrl+cmd+space = spawn fuzzel
bind.cmd+q = close-window
bind.ctrl+alt+backspace = quit

# Focus and move
bind.cmd+left = focus-column-left
bind.cmd+right = focus-column-right
bind.cmd+up = focus-window-up
bind.cmd+down = focus-window-down
bind.cmd+h = focus-column-left
bind.cmd+l = focus-column-right
bind.cmd+k = focus-window-up
bind.cmd+j = focus-window-down
bind.cmd+shift+left = move-column-left
bind.cmd+shift+right = move-column-right
bind.cmd+shift+h = move-column-left
bind.cmd+shift+l = move-column-right
bind.cmd+shift+up = move-window-up
bind.cmd+shift+down = move-window-down
bind.cmd+shift+k = move-window-up
bind.cmd+shift+j = move-window-down
bind.cmd+bracketleft = consume-or-expel-window-left
bind.cmd+bracketright = consume-or-expel-window-right

# Resize and window modes
bind.cmd+alt+left = set-column-width -10%
bind.cmd+alt+right = set-column-width +10%
bind.cmd+alt+up = set-window-height -10%
bind.cmd+alt+down = set-window-height +10%
bind.cmd+alt+h = set-column-width -10%
bind.cmd+alt+l = set-column-width +10%
bind.cmd+alt+k = set-window-height -10%
bind.cmd+alt+j = set-window-height +10%
bind.cmd+shift+space = toggle-floating
bind.cmd+r = cycle-column-width
bind.cmd+f = maximize-column
bind.cmd+shift+f = toggle-fullscreen
bind.cmd+s = toggle-stash-visible
bind.cmd+shift+s = toggle-window-stash
bind.cmd+o = toggle-overview
bind.cmd+tab = switch-column-next
bind.cmd+shift+tab = switch-column-prev

# Workspaces
bind.cmd+pageup = focus-workspace-prev
bind.cmd+pagedown = focus-workspace-next
bind.cmd+shift+pageup = move-column-to-workspace-prev
bind.cmd+shift+pagedown = move-column-to-workspace-next
bind.cmd+ctrl+shift+up = move-workspace-prev
bind.cmd+ctrl+shift+down = move-workspace-next
bind.cmd+ctrl+shift+k = move-workspace-prev
bind.cmd+ctrl+shift+j = move-workspace-next
bind.cmd+code:2 = focus-workspace 1
bind.cmd+code:3 = focus-workspace 2
bind.cmd+code:4 = focus-workspace 3
bind.cmd+code:5 = focus-workspace 4
bind.cmd+code:6 = focus-workspace 5
bind.cmd+code:7 = focus-workspace 6
bind.cmd+code:8 = focus-workspace 7
bind.cmd+code:9 = focus-workspace 8
bind.cmd+code:10 = focus-workspace 9
bind.cmd+shift+code:2 = move-column-to-workspace 1
bind.cmd+shift+code:3 = move-column-to-workspace 2
bind.cmd+shift+code:4 = move-column-to-workspace 3
bind.cmd+shift+code:5 = move-column-to-workspace 4
bind.cmd+shift+code:6 = move-column-to-workspace 5
bind.cmd+shift+code:7 = move-column-to-workspace 6
bind.cmd+shift+code:8 = move-column-to-workspace 7
bind.cmd+shift+code:9 = move-column-to-workspace 8
bind.cmd+shift+code:10 = move-column-to-workspace 9

# Monitors and scale
bind.cmd+ctrl+left = focus-monitor-left
bind.cmd+ctrl+right = focus-monitor-right
bind.cmd+ctrl+h = focus-monitor-left
bind.cmd+ctrl+l = focus-monitor-right
bind.cmd+ctrl+shift+left = move-workspace-to-monitor-left
bind.cmd+ctrl+shift+right = move-workspace-to-monitor-right
bind.cmd+ctrl+shift+h = move-workspace-to-monitor-left
bind.cmd+ctrl+shift+l = move-workspace-to-monitor-right
bind.cmd+code:13 = scale-up
bind.cmd+code:12 = scale-down
```

Per-output defaults (replace `<name>` with a connector such as `DP-1`):

```text
output.<name> = preferred
output.<name>.scale = 1
output.<name>.transform = normal
output.<name>.primary = off
output.<name>.offset = 0
output.<name>.hdr = off
output.<name>.sdr-brightness = 203
```

Outputs are arranged automatically unless `.right-of`, `.left-of`, `.above` or `.below` is set. Per-output layouts inherit `layout.*`; named workspaces inherit their output's layout and size, use the focused output unless `.monitor` is set, and have no predefined columns. Media keys have no default bindings.

See [Configuration](docs/config.md) for accepted values and workspace options.

</details>

### Automatic terminal

The command comes from `terminal`, then `$TERMINAL`, then `foot`.

| `terminal.auto-open` | Behaviour |
| --- | --- |
| `first` | Open once on the initial numbered workspace |
| `all` | Open on each visible empty workspace without predefined slots |
| `off` | Do not open automatically |

`--no-terminal` disables automatic opening for one run; `spawn-terminal` still works. Launched programs inherit `$SHELL`, `$EDITOR` and `$VISUAL`.

## Documentation

- [Configuration](docs/config.md): keys, actions, bindings and HDR.
- [Desktop integration](docs/desktop.md): sessions, clipboard, input methods, X11 apps and scripting.
- [Session locking](docs/session-lock.md): external lockers, recovery and security limits.
- [Screen capture](docs/capture.md): protocols, indicator, executable allowlist and portals.
- [Headless mode](docs/headless.md): screenshots, input scripts and input-method testing without a screen.
- [Performance](docs/performance.md): rendering, frame pacing, profiling and reference measurements.

## Contributing

Open an issue and wait for acceptance before sending a pull request, including for bug fixes. Pull requests without an accepted issue are closed.

Before submitting, run `make check`, `make race`, `make mocks-check` and `staticcheck ./...`. See [AGENTS.md](AGENTS.md) for repository rules.

## License

[GNU General Public License v3.0](LICENSE).
