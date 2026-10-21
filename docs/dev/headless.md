# Headless mode

Runs NeferWL without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

- `latest.png` shows the current frame; `frames.ffconcat` records frame timing. Every frame is also saved as `frame-NNNNNN.png`, so the directory grows.
- `--size 640x360,640x360` creates two outputs, arranged left to right,
  for testing `focus-monitor-left/right` and moving columns between outputs.
- With several outputs, each writes to `<dir>/HEADLESS-<n>/`.
- `--timeout 5s` stops NeferWL after 5 seconds.
- `grim` and recorders work too; see [Screen capture](../capture.md).

## Recording a video

Convert the screenshots and their real-time timing index into a video:

```sh
ffmpeg -f concat -safe 0 -i /tmp/shots/frames.ffconcat -vf "fps=60,format=yuv420p" -c:v libx264 -crf 18 demo.mp4
```

For a GIF, convert `demo.mp4` with ffmpeg's `palettegen` and `paletteuse` filters.

### Demo recording
Run `tools/demo/record.sh` with Go 1.27, rootless Docker, ffmpeg and network access installed.
It builds the Arch image (apps, tesseract, AT-SPI) and the compositor binary, then records the 1080p README demo.
The compositor runs headless inside a disposable container, with the binary and a work directory bind-mounted and `/dev/dri` passed for the GPU.
Firefox opens a live DuckDuckGo page, so the recording needs the network and its content varies.
A FIFO feeds `input.txt` while the script launches the apps on the same clock.
The hint step makes Firefox fullscreen and labels the page with `hint`; `hint-click <words>` then types the label of the link that shows those words, found from the scan's dump and an OCR read of the still page (`hint-read`).
Temporary captures are removed by default; set `KEEP_CAPTURE=1` to keep successful runs.
The recording and poster replace `docs/media/demo.mp4` and `docs/media/demo.png`.

## Input scripts

`--input <file>`, or `--input -` for stdin, plays one command per line:

| Command | Effect |
|---|---|
| `type <text>` | Type text |
| `key Super+Return` | Press a key, with optional Shift, Ctrl, Alt or Super |
| `hold Super+h 1s` | Press a key like `key`, hold it, then release it |
| `press Super`, `release Super` | Hold or release a modifier across mouse actions (`Super`, `Shift`, `Ctrl`, `Alt`) |
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
