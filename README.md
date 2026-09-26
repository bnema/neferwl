# NeferTTY

NeferTTY is a minimal Wayland compositor in Go with Niri-style scrollable columns. It runs on a TTY (DRM/KMS, libinput, Vulkan) or headless.

Requires Linux and Go 1.27.

## Hardware support

- **AMD GPUs:** developed and tested on AMD (radv).
- **NVIDIA GPUs:** untested. NVIDIA drivers need explicit sync (`linux-drm-syncobj`), which NeferTTY does not implement yet. Testers with NVIDIA hardware are welcome to report issues or contribute.
- **Intel GPUs:** untested.

Build: `make build` (requires sibling checkouts of purego-libwayland and purego-vulkan).

Run: `nefertty --backend=headless --timeout 5s`

## Install (Arch Linux)

```sh
make pkg                                  # builds dist/nefertty-*.pkg.tar.zst
sudo pacman -U dist/nefertty-*.pkg.tar.zst
```

The package installs `nefertty` and a **NeferTTY** session for display managers (Ly, GDM, SDDM). The session runs `nefertty --session`, which shares `WAYLAND_DISPLAY` and `DISPLAY` with D-Bus and systemd user services, so portals and notification daemons reach it. `make dist` alone builds the self-contained source tarball (Go modules vendored) that the package uses; its version ends in `.dirty` when this repo or a sibling module has uncommitted changes.

## Configuration

Configuration lives at `$XDG_CONFIG_HOME/nefertty/config` (or `~/.config/nefertty/config`). Use `nefertty --config path` to select another file; `nefertty validate-config [path]` checks it without starting the compositor.

One `key = value` per line; `#` starts a comment:

```text
keyboard.layout = fr
terminal = foot
bind.cmd+return = spawn-terminal
bind.ctrl+cmd+space = spawn fuzzel
startup = wl-paste --watch cliphist store
```

See `examples/config` for every key and its default. A missing file means defaults. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Every key applies live when the file changes, including the keyboard layout; each reload logs the keys that changed.

Building requires sibling checkouts of purego-libwayland and purego-vulkan until they are published.

## Running commands

NeferTTY starts a program, not a shell line: `spawn` runs a binary or a script. Anything more than one command with arguments, such as pipes or variables, goes in a script that you write.

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

Programs started by NeferTTY get `WAYLAND_DISPLAY`, `DISPLAY` (see [X11 apps](#x11-apps)) and `NEFERTTY_STATE` (see below). `startup` commands run once when the session starts; editing them takes effect at the next start.

## Clipboard

NeferTTY supports the clipboard (`wl_data_device`), the primary selection (middle-click paste) and `ext_data_control_v1` for clipboard managers. With [cliphist](https://github.com/sentriz/cliphist), store every copy at startup and bind a picker script (see [Running commands](#running-commands)):

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

X11 apps such as Steam and Wine run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, found in `PATH`. NeferTTY opens an X11 display, sets `DISPLAY` for the programs it starts, and runs xwayland-satellite when the first X11 app connects. If xwayland-satellite exits, the next X11 app starts it again.

```text
xwayland = xwayland-satellite   # the default; a path also works
xwayland = off                  # no X11 display
```

Do not start xwayland-satellite yourself with `startup`. Changing `xwayland` takes effect at the next start.

## State for scripts

While it runs, NeferTTY keeps its state in `$XDG_RUNTIME_DIR/nefertty/<wayland socket>.json` and passes that path to the programs it starts as `NEFERTTY_STATE`. The file lists every output (active numbered workspace, workspace count, name of the workspace on screen), the focused output and window, and every window with its app ID, PID, output and workspace.

```sh
nefertty state                        # the whole state as JSON
nefertty state output-of "$PID"       # the output of that process's window, or its nearest parent's
```

`output-of` finds application windows only; bars have no single output.

## Try it (headless)

Run `nefertty --backend=headless --screenshot /tmp/nefertty-shots`, then connect with
`WAYLAND_DISPLAY=<logged name> foot`. `/tmp/nefertty-shots/latest.png` shows the current frame.
Use `--input path` (or `--input -` for stdin) to inject a headless script. Each line is
`type text`, `key Super+Return` (also Shift, Ctrl, Alt or a bare key), `sleep 1s`, `move x y` (absolute output coordinates), `click [left|right|middle]`,
`down button`, or `up button`. Cursor rendering is not implemented yet; pointer
focus after layout changes is updated on the next move.

## Why not Rust?

Most Wayland compositors are written in C (wlroots, Sway, Mutter), C++ (KWin, Hyprland, gamescope) or Rust (niri, COSMIC). NeferTTY is written in Go, by choice:

- **Simple and readable.** Go is small and explicit. Goroutines and channels fit a compositor where each part (input, outputs, clients) owns its own state.
- **Fast builds, easy tooling.** A full build takes seconds; tests, the race detector, profiling and formatting come with the language.
- **No cgo.** NeferTTY builds with `CGO_ENABLED=0`: libwayland, Vulkan and libinput are loaded at runtime through [purego](https://github.com/ebitengine/purego).
- **Fast enough.** With care for allocations and the garbage collector, the hot paths (input, rendering, buffer handling) perform close to native code.

As a software engineer, my passion is bringing more tools to the Go ecosystem. NeferTTY grew its own libraries along the way: purego-libwayland, purego-vulkan and wlturbo, to be published for others to reuse. Building them is part of the fun.

What Go does not give us is Rust's borrow checker: memory and concurrency mistakes are not caught at compile time. NeferTTY makes up for it with strict ownership (one goroutine owns each piece of state), the race detector on every test run, and protocol tests against real Wayland clients.
