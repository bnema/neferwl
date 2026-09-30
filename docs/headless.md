# Headless mode

The headless backend runs without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

Screenshots through `grim` use `zwlr_screencopy_v1` or `ext_image_copy_capture_v1`. Both deliver opaque 8-bit sRGB, including GPU tone mapping of HDR content. Protocol capture requires Vulkan sync-file export; unsupported devices fail the request rather than wait for GPU completion on the output goroutine. The cursor is not included in captures: `overlay_cursor` and `paint_cursors` are ignored. Headless screenshot files include the cursor. If readback is temporarily unavailable, that screenshot frame is skipped without stopping the output or writing a black PNG.

Recording goes through `ext_image_copy_capture_v1`, with optional additions from `neferwl_image_capture_source_manager_v1` (XML in `internal/adapters/wayland/imagecapture/`): `create_workspace_source` (a workspace handle of `ext_workspace_manager_v1`, whether or not the workspace is on screen) and `create_output_region_source` (a rectangle of an output, in logical pixels, clipped). Both give an ordinary `ext_image_capture_source_v1`. A workspace source is the workspace's real layout without bars, not an overview preview; its buffer size follows the workspace frame (new constraints when it changes) and its sessions stop when the workspace is removed. `get_workspace_frame` gives a `neferwl_workspace_frame_v1` that sends the workspace frame (`frame`: x, y, width, height in output-local logical pixels) when created and whenever it changes, since ext-workspace carries no geometry. A region source stops when the output goes away or nothing of the rectangle is left. A workspace that is not on screen uses a separate, demand-driven child renderer and capture pipeline, with its own worker and two staging slots (up to 128 MiB host-visible memory) plus render targets (each image up to 256 MiB, 16384 pixels per side), for one workspace at a time; other sessions on a different hidden workspace wait (their frames fail) until it is gone. Hidden capture can leave the displayed workspace in direct scanout. Offscreen capture currently requires whole-frame requests matching its exact physical dimensions.

`neferwl_capture_exclusion_manager_v1` leaves a recorder's own HUD out of its frames. The recorder calls `get_exclusion` on its capture session and gets a random token (or `failed`: `busy` when another exclusion is live anywhere in the compositor, `session_stopped`). A HUD, usually on another connection of the same user, attaches up to 4 layer surfaces (top or overlay, not yet mapped, no exclusive keyboard) with the token. They stay visible over fullscreen without keyboard focus; frames of that session omit them and their popups, every other capture (wlr-screencopy included) keeps them. HUD authorization requires the token and a matching socket peer UID, never a layer namespace or app-id. The exclusion ends with its session, or when its object is destroyed; its HUD layers that are still listed then stay out of the session's frames (and no longer stay over a fullscreen window) until they are gone, so the HUD never shows in the recorder's frames after the exclusion ended. Output removal and workspace removal stop sessions. One function of the Wayland adapter (`mayCapture`) decides whether a client may capture, for wlr-screencopy and every ext-image-copy-capture source.

Offscreen frame callbacks keep clients running, but no physical presentation feedback is invented for hidden windows. The child renderer and its readback storage retire when unused and idle, are recreated for session or frame-size changes, and close with the output. Clean capture can require two compositions to preserve the displayed z-order.

`/tmp/neferwl-shots/latest.png` shows the current unlocked frame, including any HUD. During session protection, protocol captures are refused and debug PNG files stop updating; an existing file still contains an earlier unlocked frame. See [Session locking](session-lock.md). `--timeout 5s` stops NeferWL after 5 seconds.

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
