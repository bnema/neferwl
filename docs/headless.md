# Headless mode

Runs NeferWL without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

- `latest.png` shows the current frame. Every frame is also saved as `frame-NNNNNN.png`, so the directory grows.
- With several outputs, each writes to `<dir>/HEADLESS-<n>/`.
- `--timeout 5s` stops NeferWL after 5 seconds.
- `grim` and recorders work too; see [Screen capture](capture.md).

## Input scripts

`--input <file>`, or `--input -` for stdin, plays one command per line:

| Command | Effect |
|---|---|
| `type <text>` | Type text |
| `key Super+Return` | Press a key, with optional Shift, Ctrl, Alt or Super |
| `hold Super+h 1s` | Press a key like `key`, hold it, then release it |
| `sleep 1s` | Wait |
| `move <x> <y>` | Move the pointer, in output coordinates |
| `click [left\|right\|middle]`, `down <button>`, `up <button>` | Mouse buttons |
| `swipe up\|down\|left\|right` | Three-finger flick |
| `swipe4 up\|down` | Four-finger flick |

## Testing input methods

`examples/testime` sends a preedit string, then commits text, each time an app enables text input:

```sh
WAYLAND_DISPLAY=<name> go run ./examples/testime -preedit nihon -commit 日本
```

`-count <n>` exits after n commits; `-delay` sets the time between preedit and commit.

## Colour check

`make color-check` renders `examples/testpattern` on a headless output and checks the colours, in SDR and in HDR (`--headless-hdr`). It needs a GPU and `/dev/udmabuf`, and is not part of `make check`.

On a real screen, compare by eye:

```sh
WAYLAND_DISPLAY=<name> go run ./examples/testpattern -mode hdr -fullscreen   # or -mode sdr
```
