# NeferTTY

NeferTTY is a minimal Wayland compositor in Go with Niri-style scrollable columns. It runs on a TTY (DRM/KMS, libinput, Vulkan) or headless.

Requires Linux and Go 1.27.

## Hardware support

- **AMD GPUs:** developed and tested on AMD (radv).
- **NVIDIA GPUs:** untested. NVIDIA drivers need explicit sync (`linux-drm-syncobj`), which NeferTTY does not implement yet. Testers with NVIDIA hardware are welcome to report issues or contribute.
- **Intel GPUs:** untested.

Build: `make build` (requires sibling checkouts of purego-libwayland and purego-vulkan).

Run: `nefertty --backend=headless --timeout 5s`

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

## Clipboard

NeferTTY supports the clipboard (`wl_data_device`), the primary selection (middle-click paste) and `ext_data_control_v1` for clipboard managers. With [cliphist](https://github.com/sentriz/cliphist):

```text
startup = wl-paste --watch cliphist store
bind.cmd+v = spawn cliphist-pick
```

Commands are split on spaces and run without a shell, so the picker is a script on your `PATH`, for example `cliphist-pick`:

```sh
#!/bin/sh
cliphist list | fuzzel --dmenu | cliphist decode | wl-copy
```

Drag and drop is not supported yet.

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
