# NeferTTY

NeferTTY is a minimal Wayland compositor in Go with Niri-style scrollable columns. This repository currently contains only the skeleton and logging.

Requires Linux and Go 1.27.

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
```

See `examples/config` for every key and its default. A missing file means defaults. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Every key applies live when the file changes, including the keyboard layout; each reload logs the keys that changed.

Building requires sibling checkouts of purego-libwayland and purego-vulkan until they are published.

## Try it (headless)

Run `nefertty --backend=headless --screenshot /tmp/nefertty-shots`, then connect with
`WAYLAND_DISPLAY=<logged name> foot`. `/tmp/nefertty-shots/latest.png` shows the current frame.
Use `--input path` (or `--input -` for stdin) to inject a headless script. Each line is
`type text`, `key Super+Return` (also Shift, Ctrl, Alt or a bare key), `sleep 1s`, `move x y` (absolute output coordinates), `click [left|right|middle]`,
`down button`, or `up button`. Cursor rendering is not implemented yet; pointer
focus after layout changes is updated on the next move.
