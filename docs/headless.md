# Headless mode

The headless backend runs without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

- `/tmp/neferwl-shots/latest.png` shows the current frame, cursor included. A frame that cannot be read back is skipped, never written black.
- `--timeout 5s` stops NeferWL after 5 seconds.
- While the session is locked, the PNG stops updating and keeps the last unlocked frame. See [Session locking](session-lock.md).
- The cursor is not drawn on screen. After a layout change, pointer focus updates on the next move.

`grim` and recorders also work here; see [Screen capture](capture.md).

## Input scripts

`--input <path>`, or `--input -` for stdin, plays one command per line:

| Command | Effect |
|---|---|
| `type <text>` | Type text |
| `key Super+Return` | Press a key, with optional Shift, Ctrl, Alt or Super |
| `sleep 1s` | Wait |
| `move <x> <y>` | Move the pointer, in output coordinates |
| `click [left\|right\|middle]`, `down <button>`, `up <button>` | Mouse buttons |
| `swipe up\|down\|left\|right` | Quick three-finger touchpad flick |
| `swipe4 up\|down` | Four-finger flick |

## Testing input methods

`examples/testime` is a minimal input method. Each time an app enables text input, it sends a preedit string, then commits the final text:

```sh
neferwl --backend=headless
WAYLAND_DISPLAY=<name from the log> go run ./examples/testime -preedit nihon -commit 日本
```

`-count <n>` exits after n commits; `-delay` sets the time between preedit and commit.

## Colour check

`make color-check` checks that known colours come out right. It needs a GPU and `/dev/udmabuf`, and is not part of `make check` or CI (build tag `colorcheck`, test in `internal/app/colorcheck_test.go`). It runs a headless 640×360 output with `examples/testpattern -fullscreen` and compares 5×5 boxes at the centre of each patch:

- `TestColorCheckSDR`: an sRGB `wl_shm` client on an SDR output. `latest.png` must match the sRGB values within ±1 per channel.
- `TestColorCheckHDR`: a virtual HDR output (`--headless-hdr`), read back as raw PQ codes in `latest-pq.png`. An sRGB client must give `PQ(BT.709→BT.2020 · linear · 203 nits)`; a PQ client (`-mode hdr`) its own codes. Tolerance is ±0.012 PQ, or 0.03 nit for channels below 0.05 nit, where PQ is very steep. The test fails if the output falls back to SDR.

`--screenshot-raw`, with `--screenshot` and `--headless-hdr`, also writes `latest-pq.png`: a 16-bit PNG of the 10-bit PQ codes, without cursor or tone mapping. It is a test-only flag.

The check cannot see hardware scanout or the KMS colour pipeline. On a real HDR screen, run `WAYLAND_DISPLAY=<name> go run ./examples/testpattern -mode hdr -fullscreen` (or `-mode sdr`) and compare by eye. The client asks for fullscreen 1.2 s after its first commit, because earlier requests are ignored.
