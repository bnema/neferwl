<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue?style=flat-square" alt="License: GPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://badgen.net/github/last-commit/bnema/neferwl/main?icon=github" alt="Last commit"></a>
  <a href="https://github.com/bnema/neferwl/stargazers"><img src="https://badgen.net/github/stars/bnema/neferwl?icon=github" alt="GitHub stars"></a>
</p>

<p align="center">A Wayland compositor for people who live in a terminal and die in games.</p>

> [!WARNING]
> **Early alpha.** NeferWL is developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested; expect bugs and breaking config changes.

---

## Why NeferWL

NeferWL has no animations, themes, built-in bar or wallpaper, by design. The compositor stays small so the CPU and GPU go to your terminal and your games.

- **Terminal first.** NeferWL opens your terminal at startup. Bars, launchers and notifications are external clients such as Waybar, fuzzel and mako.
- **Column tiling.** Up to `max-columns` windows share the screen, as in Sway. Past that, new columns scroll to the right as in Niri, or split in a spiral with `overflow = fixed`. Each workspace can set its own rules.
- **Custom workspaces.** Numbered workspaces appear and disappear as you use them. Named workspaces are outside the numbered list and shown through a bind. **Slots** declare a workspace's columns (width and command): NeferWL starts the apps in the background and puts each window in its column.
- **Multi-monitor.** Each monitor has its own workspaces. When you unplug a monitor, its workspaces move to another one; when you plug it back, they return, and your focus stays where it was.
- **Games.** A fullscreen game is shown without composition, with tearing and VRR when the game asks for them. Details are in [the performance path](#the-performance-path).
- **Low footprint.** About 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change.
- **Live config.** One `key = value` file. Every key applies when you save, keyboard layout included, and windows stay open.

An example config:

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

### The performance path

These protocols and kernel features cut copies, waits and latency between the app and the screen:

- **Zero copy.** GPU clients (`zwp_linux_dmabuf_v1` v4) are sampled in place. Vulkan composes straight into exported scanout images, and the frame fence goes to KMS as `IN_FENCE_FD`: no CPU copy, no CPU wait before a flip.
- **Direct scanout.** A fullscreen window's buffer goes straight to the display plane; dmabuf feedback sends the game a scanout-ready format first. A lone opaque window can use an overlay plane.
- **Tearing and VRR.** `wp_tearing_control_v1` flips without waiting for vblank when the game requests it; variable refresh runs while a buffer is scanned out.
- **Explicit sync.** `wp_linux_drm_syncobj_v1` passes GPU fences from the app to KMS. NVIDIA drivers need it.
- **Frame pacing.** Apps get real flip times through `wp_presentation`, and can queue frames with `wp_fifo_v1` and `wp_commit_timing_v1`.
- **Only what changed.** NeferWL redraws only damaged regions, skips surfaces hidden behind opaque ones, and leaves idle outputs alone. Frame, cursor, overlay and VRR go to the kernel in one atomic commit.
- **Input.** Relative pointer, pointer constraints and keyboard shortcuts inhibit for games. Input at 1 kHz and more never waits on rendering, and the cursor has its own hardware plane.
- **X11 games.** Steam and Wine run through xwayland-satellite, started on the first X11 connection.

## Hardware support

NeferWL runs on Linux. It needs a GPU and kernel driver with atomic KMS, and a Vulkan 1.3 device that can export images as dmabufs. Without them, it stops at startup with an error.

- **AMD:** developed and tested on radv.
- **NVIDIA:** untested. Explicit sync, which NVIDIA drivers need, is implemented. Reports from NVIDIA users are welcome.
- **Intel:** untested.

## Install (Arch Linux)

Building needs Go 1.27.

```sh
make pkg                                  # builds dist/neferwl-*.pkg.tar.zst from HEAD
sudo pacman -U dist/neferwl-*.pkg.tar.zst
```

The package installs `neferwl` and a **NeferWL** session for display managers such as Ly, GDM and SDDM. The session runs `neferwl --session`, which passes `WAYLAND_DISPLAY` and `DISPLAY` to D-Bus and systemd user services so that portals and notification daemons can connect.

On other distributions, `make build` builds `bin/neferwl`.

## Configuration

NeferWL reads `$XDG_CONFIG_HOME/neferwl/config`, or `~/.config/neferwl/config`. `neferwl --config <path>` selects another file, and `neferwl validate-config [path]` checks a file without starting the compositor.

The file has one `key = value` per line, and `#` starts a comment. A missing file or key means the default. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Changes apply when you save, and each reload logs the keys that changed.

[docs/config.md](docs/config.md) lists every key, action and default bind. `examples/config` is a commented copy of the defaults.

## Running commands

`spawn <command>` binds, `startup = <command>` lines and `terminal = <command>` run one program directly, without a shell. NeferWL splits the line on spaces: the first word is the program, found in `PATH` or given as a path such as `/opt/tool/run`, and the other words are its arguments.

```text
bind.ctrl+cmd+space = spawn fuzzel
startup = waybar
startup = wl-paste --watch cliphist store
```

Pipes (`|`), `&&`, redirections, variables (`$HOME`), `~` and quotes have no special meaning: they reach the program as plain arguments. For those, write a script and run it:

```sh
#!/bin/sh
# ~/.local/bin/screenshot-area: select an area, save it and copy it.
file="$HOME/Pictures/$(date +%F-%T).png"
grim -g "$(slurp)" "$file" && wl-copy < "$file"
```

```text
bind.cmd+shift+s = spawn screenshot-area
```

Make the script executable (`chmod +x`) and put it in a directory of your `PATH`, such as `~/.local/bin`. It can use any shell, including fish (`#!/usr/bin/env fish`).

Programs started by NeferWL get `WAYLAND_DISPLAY`, `DISPLAY` (see [X11 apps](#x11-apps)) and `NEFERWL_STATE` (see below). `startup` commands run once when the session starts, so editing them takes effect at the next start.

## Clipboard

NeferWL supports the clipboard (`wl_data_device`), the primary selection (middle-click paste) and `ext_data_control_v1` for clipboard managers. To keep a history with [cliphist](https://github.com/sentriz/cliphist), store every copy at startup and bind a picker script:

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

X11 apps such as Steam and Wine run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, found in `PATH`. NeferWL opens an X11 display, sets `DISPLAY` for the programs it starts, and starts xwayland-satellite when the first X11 app connects. If xwayland-satellite exits, the next X11 app starts it again.

```text
xwayland = xwayland-satellite   # the default; a path also works
xwayland = off                  # no X11 display
```

Do not start xwayland-satellite yourself with `startup`. Changing `xwayland` takes effect at the next start.

## Idle and screen off

NeferWL supports `ext_idle_notifier_v1` and `zwlr_output_power_management_v1`, so [swayidle](https://github.com/swaywm/swayidle) and [wlopm](https://git.sr.ht/~leon_plickat/wlopm) turn the screens off after a delay. A window that inhibits idle, such as a video player or a game, keeps them on.

```text
startup = swayidle -w timeout 300 wlopm-off resume wlopm-on
```

`swayidle` runs its commands through a shell; NeferWL does not, so the quoted form `timeout 300 'wlopm --off "*"'` does not fit on a `startup` line. Put each command in a script (see [Running commands](#running-commands)):

```sh
#!/bin/sh
# ~/.local/bin/wlopm-off (wlopm-on is the same with --on)
exec wlopm --off '*'
```

## State for scripts

While it runs, NeferWL writes its state to `$XDG_RUNTIME_DIR/neferwl/<wayland socket>.json` and passes that path to the programs it starts as `NEFERWL_STATE`. The file lists:

- every output, with its active numbered workspace, workspace count and the name of the workspace on screen;
- the focused output and window;
- every window, with its app ID, PID, output and workspace.

```sh
neferwl state                        # the whole state as JSON
neferwl state output-of "$PID"       # the output of that process's window, or its nearest parent's
```

`output-of` finds application windows only, because a bar has no single output.

## Try it (headless)

The headless backend runs without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

Screenshots through `grim` are verified with `zwlr_screencopy_v1` and `ext_image_copy_capture_v1`. The cursor is not included in captures: `overlay_cursor` and `paint_cursors` are ignored. Headless screenshot files include the cursor.

`/tmp/neferwl-shots/latest.png` shows the current frame. `--timeout 5s` stops NeferWL after 5 seconds.

`--input <path>`, or `--input -` for stdin, plays an input script. One command per line:

- `type <text>`
- `key Super+Return` (Shift, Ctrl and Alt also work, as does a bare key)
- `sleep 1s`
- `move <x> <y>` (output coordinates)
- `click [left|right|middle]`, `down <button>`, `up <button>`

The headless backend does not draw the cursor on screen. After a layout change, pointer focus updates on the next move.

## Why not Rust?

Most Wayland compositors are written in C (wlroots, Sway, Mutter), C++ (KWin, Hyprland, gamescope) or Rust (niri, COSMIC). NeferWL uses Go on purpose:

- **Readable.** Go is small and explicit. Goroutines and channels fit a compositor where input, each output and the clients own their own state.
- **Tooling.** A full build takes seconds, and tests, the race detector, profiling and formatting come with the language.
- **No cgo.** NeferWL builds with `CGO_ENABLED=0`: libwayland, Vulkan and libinput are loaded at runtime through [purego](https://github.com/ebitengine/purego).
- **Speed.** With care for allocations and the garbage collector, input, rendering and buffer handling run close to native code.

I like bringing more tools to the Go ecosystem. NeferWL grew its own libraries along the way, [purego-libwayland](https://github.com/bnema/purego-libwayland), [purego-vulkan](https://github.com/bnema/purego-vulkan) and [wlturbo](https://github.com/bnema/wlturbo), and other Go projects can use them.

Go has no borrow checker, so the compiler does not catch memory and concurrency mistakes as Rust's does. NeferWL compensates with strict ownership (one goroutine owns each piece of state), the race detector on every test run, and protocol tests against real Wayland clients.

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
