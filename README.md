# NeferTTY

NeferTTY is a minimal Wayland compositor in Go with Niri-style scrollable columns. This repository currently contains only the skeleton and logging.

Requires Linux and Go 1.27.

Build: `make build` (requires sibling checkouts of purego-libwayland and purego-vulkan).

Run: `nefertty --backend=headless --timeout 5s`

## Configuration

Configuration lives at `$XDG_CONFIG_HOME/nefertty/config.toml` (or `~/.config/nefertty/config.toml`). Use `nefertty --config path` to select another file; `nefertty validate-config [path]` checks it without starting the compositor.

```toml
[terminal]
command = ["foot"]
[layout]
gaps = 8
```

See `examples/config.toml` for all defaults.

Building requires sibling checkouts of purego-libwayland and purego-vulkan until they are published.

## Try it (headless)

Run `nefertty --backend=headless`, then connect with
`WAYLAND_DISPLAY=<logged name> foot`. The headless backend does not render yet.
