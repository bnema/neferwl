<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue?style=flat-square" alt="License: GPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://badgen.net/github/last-commit/bnema/neferwl/main?icon=github" alt="Last commit"></a>
  <a href="https://github.com/bnema/neferwl/stargazers"><img src="https://badgen.net/github/stars/bnema/neferwl?icon=github" alt="GitHub stars"></a>
</p>

<p align="center"><em>Simple. Bare. Fast.</em></p>

<p align="center">A minimal Wayland compositor in Go, built for performance: scrollable columns, multi-monitor workspaces and a zero-copy path for games.</p>

> [!WARNING]
> **Early alpha.** NeferWL is developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested; expect bugs and breaking config changes.

---

## Why NeferWL

NeferWL does a few things and does them fast. No animations, no themes, no built-in bar or wallpaper: those are choices, not missing features. Every frame budget goes to your apps.

- **A terminal, then out of the way.** One workspace opens your terminal at startup. Bars, launchers and notifications are external clients (Waybar, fuzzel, mako…).
- **Columns between tiling and scrolling.** Up to `max-columns` windows share the screen like a classic tiling WM (Sway); past that, columns scroll to the right like Niri, or split in a spiral with `overflow = fixed`. Each workspace picks its own rules.
- **Workspaces you design.** Numbered workspaces are dynamic. Named workspaces can be hidden behind a bind, and **slots** declare their columns (width + command): NeferWL starts the apps in the background and puts each window in its place.
- **Multi-monitor done right.** Each monitor owns its workspaces. Unplug one and its workspaces move to another monitor; plug it back and they return home, without stealing your focus.
- **Built for games.** Fullscreen games skip composition entirely (details below), with tearing and VRR when the game asks.
- **Light.** A running session uses about 40–80 MB of RAM with two 4K monitors, and near-zero CPU when nothing changes on screen.
- **Live config.** One flat `key = value` file. Every key applies on save, keyboard layout included; windows stay open.

A complete desktop in a few lines:

```text
keyboard.layout = fr
terminal = foot
startup = waybar

output.DP-1 = 3840x2160@144
output.DP-1.scale = 1.5

layout.max-columns = 3

# A hidden dev workspace: editor and terminal, started and placed for you.
workspace.dev.hidden = on
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 33%, foot
bind.cmd+d = workspace dev
```

### The performance path

Every Wayland protocol NeferWL implements is there because it saves work or latency:

- **Zero copy.** GPU clients (`zwp_linux_dmabuf_v1` v4) are sampled in place. Vulkan composes straight into exported scanout images, and the frame fence goes to KMS as `IN_FENCE_FD`: no CPU copy, no CPU wait before a flip.
- **Direct scanout.** A fullscreen window's buffer goes straight to the display plane; dmabuf feedback sends the game a scanout-ready format first. A lone opaque window can use an overlay plane.
- **Tearing and VRR.** `wp_tearing_control_v1` flips without waiting for vblank when the game requests it; variable refresh runs while a buffer is scanned out.
- **Explicit sync.** `wp_linux_drm_syncobj_v1` passes GPU fences end to end (needed by NVIDIA).
- **Frame pacing.** `wp_presentation` from kernel flip timestamps, `wp_fifo_v1`, `wp_commit_timing_v1` and `wp_content_type_v1`.
- **Only what changed.** Damage-limited redraws, occlusion culling of hidden surfaces, idle outputs never redraw. Atomic KMS: frame, cursor, overlay and VRR in one commit.
- **Input for games.** Relative pointer, pointer constraints, keyboard shortcuts inhibit, 1 kHz+ non-blocking input, hardware cursor plane.
- **X11 games.** Steam and Wine run through xwayland-satellite, started on the first X11 connection.

Requires Linux and Go 1.27.

## Hardware support

NeferWL needs a GPU and kernel driver with atomic KMS and a Vulkan 1.3 device that can export images as dmabufs; without them it stops at startup with an error. Frames are composed on the GPU only: no CPU copy and no CPU wait for the GPU before a page flip.

- **AMD GPUs:** developed and tested on AMD (radv).
- **NVIDIA GPUs:** untested. Explicit sync (`linux-drm-syncobj`), which NVIDIA drivers need, is implemented. Testers with NVIDIA hardware are welcome to report issues or contribute.
- **Intel GPUs:** untested.

Build: `make build`.

Run: `neferwl --backend=headless --timeout 5s`

## Install (Arch Linux)

```sh
make pkg                                  # builds dist/neferwl-*.pkg.tar.zst from HEAD
sudo pacman -U dist/neferwl-*.pkg.tar.zst
```

The package installs `neferwl` and a **NeferWL** session for display managers (Ly, GDM, SDDM). The session runs `neferwl --session`, which shares `WAYLAND_DISPLAY` and `DISPLAY` with D-Bus and systemd user services, so portals and notification daemons reach it.

## Developing the bindings

NeferWL depends on tagged releases of [purego-libwayland](https://github.com/bnema/purego-libwayland) and [purego-vulkan](https://github.com/bnema/purego-vulkan). To change them together with NeferWL, check them out next to it and use a Go workspace (not committed):

```sh
go work init . ../purego-libwayland ../purego-vulkan
```

`make pkg` ignores the workspace and builds the versions in `go.mod`: tag a binding change and update `go.mod` before packaging it.

## Configuration

Configuration lives at `$XDG_CONFIG_HOME/neferwl/config` (or `~/.config/neferwl/config`). Use `neferwl --config path` to select another file; `neferwl validate-config [path]` checks it without starting the compositor.

One `key = value` per line; `#` starts a comment:

```text
keyboard.layout = fr
terminal = foot
bind.cmd+return = spawn-terminal
bind.ctrl+cmd+space = spawn fuzzel
startup = wl-paste --watch cliphist store
```

Every key, value and default bind is listed in [docs/config.md](docs/config.md); `examples/config` is a commented copy of the defaults. A missing file means defaults. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Every key applies live when the file changes, including the keyboard layout; each reload logs the keys that changed.

## Running commands

NeferWL starts a program, not a shell line: `spawn` runs a binary or a script. Anything more than one command with arguments, such as pipes or variables, goes in a script that you write.

`spawn <command>` binds, `startup = <command>` lines and `terminal = <command>` run one program directly, without a shell. The line is split on spaces: the first word is the program (looked up in `PATH`, or a path such as `/opt/tool/run`), the other words are its arguments.

```text
bind.ctrl+cmd+space = spawn fuzzel
startup = waybar
startup = wl-paste --watch cliphist store
```

There is no shell, so pipes (`|`), `&&`, redirections, variables (`$HOME`), `~` and quotes have no special meaning: they reach the program as plain arguments. What you write is what runs.

For anything more complex, write a script and run the script:

```sh
#!/bin/sh
# ~/.local/bin/screenshot-area: select an area, save it and copy it.
file="$HOME/Pictures/$(date +%F-%T).png"
grim -g "$(slurp)" "$file" && wl-copy < "$file"
```

```text
bind.cmd+shift+s = spawn screenshot-area
```

Make it executable (`chmod +x`) and put it in a directory of your `PATH`, such as `~/.local/bin`. The script can use any shell, including fish (`#!/usr/bin/env fish`).

Programs started by NeferWL get `WAYLAND_DISPLAY`, `DISPLAY` (see [X11 apps](#x11-apps)) and `NEFERWL_STATE` (see below). `startup` commands run once when the session starts; editing them takes effect at the next start.

## Clipboard

NeferWL supports the clipboard (`wl_data_device`), the primary selection (middle-click paste) and `ext_data_control_v1` for clipboard managers. With [cliphist](https://github.com/sentriz/cliphist), store every copy at startup and bind a picker script (see [Running commands](#running-commands)):

```text
startup = wl-paste --watch cliphist store
bind.cmd+v = spawn cliphist-pick
```

`~/.local/bin/cliphist-pick`:

```sh
#!/bin/sh
cliphist list | fuzzel --dmenu | cliphist decode | wl-copy
```

Drag and drop is not supported yet.

## X11 apps

X11 apps such as Steam and Wine run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, found in `PATH`. NeferWL opens an X11 display, sets `DISPLAY` for the programs it starts, and runs xwayland-satellite when the first X11 app connects. If xwayland-satellite exits, the next X11 app starts it again.

```text
xwayland = xwayland-satellite   # the default; a path also works
xwayland = off                  # no X11 display
```

Do not start xwayland-satellite yourself with `startup`. Changing `xwayland` takes effect at the next start.

## State for scripts

While it runs, NeferWL keeps its state in `$XDG_RUNTIME_DIR/neferwl/<wayland socket>.json` and passes that path to the programs it starts as `NEFERWL_STATE`. The file lists every output (active numbered workspace, workspace count, name of the workspace on screen), the focused output and window, and every window with its app ID, PID, output and workspace.

```sh
neferwl state                        # the whole state as JSON
neferwl state output-of "$PID"       # the output of that process's window, or its nearest parent's
```

`output-of` finds application windows only; bars have no single output.

## Try it (headless)

Run `neferwl --backend=headless --screenshot /tmp/neferwl-shots`, then connect with
`WAYLAND_DISPLAY=<logged name> foot`. `/tmp/neferwl-shots/latest.png` shows the current frame.
Use `--input path` (or `--input -` for stdin) to inject a headless script. Each line is
`type text`, `key Super+Return` (also Shift, Ctrl, Alt or a bare key), `sleep 1s`, `move x y` (absolute output coordinates), `click [left|right|middle]`,
`down button`, or `up button`. Cursor rendering is not implemented yet; pointer
focus after layout changes is updated on the next move.

## Why not Rust?

Most Wayland compositors are written in C (wlroots, Sway, Mutter), C++ (KWin, Hyprland, gamescope) or Rust (niri, COSMIC). NeferWL is written in Go, by choice:

- **Simple and readable.** Go is small and explicit. Goroutines and channels fit a compositor where each part (input, outputs, clients) owns its own state.
- **Fast builds, easy tooling.** A full build takes seconds; tests, the race detector, profiling and formatting come with the language.
- **No cgo.** NeferWL builds with `CGO_ENABLED=0`: libwayland, Vulkan and libinput are loaded at runtime through [purego](https://github.com/ebitengine/purego).
- **Fast enough.** With care for allocations and the garbage collector, the hot paths (input, rendering, buffer handling) perform close to native code.

As a software engineer, my passion is bringing more tools to the Go ecosystem. NeferWL grew its own libraries along the way: purego-libwayland, purego-vulkan and wlturbo, to be published for others to reuse. Building them is part of the fun.

What Go does not give us is Rust's borrow checker: memory and concurrency mistakes are not caught at compile time. NeferWL makes up for it with strict ownership (one goroutine owns each piece of state), the race detector on every test run, and protocol tests against real Wayland clients.

## Contributing

Contributions are welcome. Open an issue first, for a bug fix or a feature, and wait until it is accepted before sending a pull request. Pull requests without an accepted issue will be closed.

Before a pull request, `make check`, `make race`, `make mocks-check` and `staticcheck ./...` must pass. Repository rules are in [AGENTS.md](AGENTS.md).

## License

NeferWL is licensed under the [GNU General Public License v3.0](LICENSE).
