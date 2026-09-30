# Headless mode

The headless backend runs without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

Screenshots through `grim` use `zwlr_screencopy_v1` or `ext_image_copy_capture_v1`. Both deliver opaque 8-bit sRGB, including GPU tone mapping of HDR content. Protocol capture requires Vulkan sync-file export; unsupported devices fail the request rather than wait for GPU completion on the output goroutine. The cursor is not included in captures: `overlay_cursor` and `paint_cursors` are ignored. Headless screenshot files include the cursor. If readback is temporarily unavailable, that screenshot frame is skipped without stopping the output or writing a black PNG.

Nefercap uses the private `neferwl_capture_manager_v1` protocol for recording sessions. A session targets an output, a fixed output-local region or a workspace's stable identity. Workspace recording uses the workspace's real layout rather than overview previews. While hidden, it uses a separate, demand-driven child renderer and capture pipeline, with its own worker and two staging slots (up to 128 MiB host-visible memory) plus render targets (each image up to 256 MiB, 16384 pixels per side). Hidden capture can leave the displayed workspace in direct scanout. Offscreen capture currently requires a whole-workspace target and requests matching its exact physical dimensions; cropped targets or requests are unsupported.

The compositor draws a red border around the visible recording target. An explicitly authorized HUD stays visible over fullscreen without keyboard focus. The session's captures omit the border, HUD and its popups; other clients' standard captures include them. HUD authorization requires the session's random token and a matching socket peer UID, never a layer namespace or app-id. Output removal, power-off, owner disconnect and a five-second keepalive timeout stop the session.

Offscreen frame callbacks keep clients running, but no physical presentation feedback is invented for hidden windows. The child renderer and its readback storage retire when unused and idle, are recreated for session or frame-size changes, and close with the output. Clean capture can require two compositions to preserve the displayed z-order.

`/tmp/neferwl-shots/latest.png` shows the current unlocked frame, including the recording border and HUD. During session protection, protocol captures are refused and debug PNG files stop updating; an existing file still contains an earlier unlocked frame. See [Session locking](session-lock.md). `--timeout 5s` stops NeferWL after 5 seconds.

`--input <path>`, or `--input -` for stdin, plays an input script. One command per line:

- `type <text>`
- `key Super+Return` (Shift, Ctrl and Alt also work, as does a bare key)
- `sleep 1s`
- `move <x> <y>` (output coordinates)
- `click [left|right|middle]`, `down <button>`, `up <button>`
- `swipe up|down|left|right` (a quick three-finger touchpad flick); `swipe4 up|down` is a four-finger one

`examples/testime` is a minimal input method for testing text input. Each time an app enables a text input, it sends a preedit string, then commits the final text:

```sh
neferwl --backend=headless
WAYLAND_DISPLAY=<name from the log> go run ./examples/testime -preedit nihon -commit 日本
```

`-count <n>` exits after n commits, and `-delay` sets the time between preedit and commit.

The headless backend does not draw the cursor on screen. After a layout change, pointer focus updates on the next move.
