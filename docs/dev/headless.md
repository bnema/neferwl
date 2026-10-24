# Headless mode

Runs NeferWL without a screen, for tests and quick checks:

```sh
neferwl --backend=headless --screenshot /tmp/neferwl-shots
WAYLAND_DISPLAY=<name from the log> foot
```

- `latest.png` shows the current frame; `frames.ffconcat` records frame timing. Every frame is also saved as `frame-NNNNNN.png`, so the directory grows. `latest.png` is a hard link to the newest frame, so replace it rather than editing it in place.
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
Run `tools/demo/record.sh`. It requires Go 1.27, rootless Docker, ffmpeg, network access and an AMD GPU.
It builds the Arch image (apps, tesseract, AT-SPI) and the compositor binary, then records the 1080p README demo.
The compositor runs headless inside a disposable container, with the binary and a work directory bind-mounted and `/dev/dri` passed for the GPU.
The image only carries Mesa's radeon Vulkan driver, and hint scans need the sync files that a hardware driver exports; the script stops early without an AMD render node.
Firefox opens a live DuckDuckGo page, so the recording needs the network and its content varies.
A FIFO feeds `input.txt` while the script launches the apps on the same clock.
The hint step makes Firefox fullscreen and labels the page with `hint`; `hint-click <words>` then types the label of the link that shows those words, found from the scan's dump and an OCR read of the still page (`hint-read`).
The recording only counts if the compositor stops on the quit key that ends `input.txt`; reaching its `--timeout` first fails the run as truncated.
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

## Ordered hint sources

Hint capture needs a hardware render node exporting sync files (the AMD backend
used by `tools/demo/record.sh` works). Install foot, a browser or GTK client,
`tesseract` with English data, and AT-SPI with `dbus-run-session`. Detector runs
also need the `nefer-hints` binary, models and preferably Vulkan.

Build with Go 1.27:

```sh
T=/tmp/hs-src
mkdir -p "$T"
CGO_ENABLED=0 go build -o "$T/neferwl" ./cmd/neferwl
```

Use a throwaway config with `hint.enable = on`, `hint.capture = on`,
`log.debug = hints`, `bind.cmd+g = hint`, and one of the chains below. Set
`hint.detector` and `hint.model` to the installed binary and model directory.
For the test installation these are
`~/.local/share/nefer-hints-test/bin/nefer-hints` and
`~/.local/share/nefer-hints-test/models`.

```sh
env -u NEFERWL_STATE -u NEFERWL_SLOT XDG_STATE_HOME="$T/state" \
  NEFERWL_HINT_DEBUG=1 dbus-run-session -- bash -c '
  T=$1
  # Enable accessibility before either the compositor or its clients starts.
  gdbus call --session --dest org.a11y.Bus --object-path /org/a11y/bus \
    --method org.freedesktop.DBus.Properties.Set org.a11y.Status IsEnabled "<true>"
  # On hosts whose registry service activation is unavailable (adjust path).
  /usr/lib/at-spi2-registryd >"$T/registry.log" 2>&1 &
  WAYLAND_DISPLAY=wp4-headless "$T/neferwl" --backend=headless \
    --config "$T/config" --no-xwayland --screenshot "$T/shots" \
    --input "$T/input" --timeout 20s & compositor=$!
  # Bounded wait for the actual display, not an inherited live-session display.
  for i in {1..50}; do
    display=$(jq -r "select(.message == \"listening\") | .WAYLAND_DISPLAY" \
      "$T/state/neferwl/runs/headless/latest.log" 2>/dev/null | head -1)
    [ -n "$display" ] && break
    sleep .1
  done
  [ -n "$display" ] || exit 1
  export WAYLAND_DISPLAY="$display"
  foot sh -c "printf https://example.org/a; exec sleep 60" &
  chromium --ozone-platform=wayland --force-renderer-accessibility \
    --user-data-dir="$T/chrome" "file://$T/page.html" >"$T/chromium.log" 2>&1 &
  wait "$compositor"
' bash "$T"
```

All clients and state queries must run inside that wrapper shell so they share
its session bus as well as its Wayland display. Logs are in
`$T/state/neferwl/runs/headless/latest.log`.
Prepare `$T/page.html` with local links before running. Arrange both windows
visibly; open a menu if possible.
Script `sleep 8s`, `key Super+g`, `sleep 4s`, then `type <visible label>`.
Check screenshots before and after the click; a local link changing the page
background is a convenient observable result.

| `hint.sources` | Verify in logs and dump JSON |
|---|---|
| `atspi, ocr` | Accessible window covered by AT-SPI, terminal passed to OCR; no detector startup |
| `detector` | No AT-SPI connection or OCR stage; one detector request/crop per window |
| `atspi, detector` | Covered accessible window skipped by detector; no OCR stage |

`hint stage` logs give source, window count, targets and milliseconds. JSON
under `$T/state/neferwl/hints/` retains input window IDs, coverage, popup crops,
errors and target sources. Preserve logs, scripts, dumps and labelled screenshots
with a short results README, including timings and any unverified assertions.
An AT-SPI registry activation failure is a failed accessibility check, not proof
of correct mixed-source coverage. Check that the session registry is running.
Also test a missing detector binary (scan completes without labels) and duplicate
sources (startup rejects `hint.sources = ocr, ocr`).
