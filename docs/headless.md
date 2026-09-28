# Headless mode

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

`examples/testime` is a minimal input method for testing text input. Each time an app enables a text input, it sends a preedit string, then commits the final text:

```sh
neferwl --backend=headless
WAYLAND_DISPLAY=<name from the log> go run ./examples/testime -preedit nihon -commit 日本
```

`-once` exits after the first commit, and `-delay` sets the time between preedit and commit.

The headless backend does not draw the cursor on screen. After a layout change, pointer focus updates on the next move.
