# Headless testing

Test user-facing changes on an isolated headless session before merging. The
session has its own Wayland display, control socket and logs, so the desktop
stays untouched. [Scripting and agents](../control.md) documents every control
command; this page covers testing NeferWL itself.

## End-to-end recipe

```sh
T=$(mktemp -d)
CGO_ENABLED=0 go build -o "$T/neferwl" ./cmd/neferwl
printf 'animations = off\n' >"$T/config"
"$T/neferwl" session new --id test --config "$T/config" --ttl 60s -- --no-xwayland
"$T/neferwl" -s test key Super+Return --until 'window-mapped app_id=foot'
"$T/neferwl" -s test state | jq '.windows[] | {id, app_id, column, row}'
"$T/neferwl" -s test shot
"$T/neferwl" session rm test
```

- Use the branch binary for every command and `-s test` for every control
  command.
- `--size 640x360,640x360` creates two outputs, left to right.
- With animations on, keep the default `--wait settled` before reading
  geometry.
- Launch external clients with the `wayland` display from the readiness JSON.
- Logs: `$XDG_STATE_HOME/neferwl/sessions/test/latest.log`.
- Record a sequence with `shot --every-frame` and `shot --stop`; see
  [Every frame and video](../control.md#every-frame-and-video).

## Demo recording
Run `tools/demo/record.sh`. It requires Go 1.27, rootless Docker, ffmpeg, network access and an AMD GPU.
It builds the Arch image (apps, tesseract, AT-SPI) and the compositor binary, then records the 1080p README demo.
The compositor runs headless inside a disposable container, with the binary and a work directory bind-mounted and `/dev/dri` passed for the GPU.
The image only carries Mesa's radeon Vulkan driver, and hint scans need the sync files that a hardware driver exports; the script stops early without an AMD render node.
Firefox opens a live DuckDuckGo page, so the recording needs the network and its content varies.
The recording script translates `input.txt` into control-socket commands while launching apps on the same clock.
The hint step makes Firefox fullscreen and labels the page with `hint`; `hint-click <words>` then types the label of the link that shows those words, found from the scan's dump and an OCR read of the still page (`hint-read`).
The recording only counts if the compositor stops on the quit key that ends `input.txt`; reaching its lifetime limit first fails the run as truncated.
Temporary captures are removed by default; set `KEEP_CAPTURE=1` to keep successful runs.
The recording and poster replace `docs/media/demo.mp4` and `docs/media/demo.png`.

## Testing input methods

`examples/testime` sends a preedit string, then commits text, each time an app enables text input:

```sh
WAYLAND_DISPLAY=<name> go run ./examples/testime -preedit nihon -commit 日本
```

`-count <n>` exits after n commits; `-delay` sets the time between preedit and commit.

## Testing the virtual pointer

`neferwl pointer` drives the pointer of the session whose `WAYLAND_DISPLAY` it runs with:

```sh
WAYLAND_DISPLAY=<name> neferwl pointer abs 0.1 0.5 click
WAYLAND_DISPLAY=<name> neferwl pointer drag 100 0 scroll down 3
WAYLAND_DISPLAY=<name> neferwl pointer --output HEADLESS-2 abs 0.5 0.5
```

Check the result with `neferwl -s <id> state` (`.window.id` is the focused window). Single shots do not show the cursor; `--every-frame` frames do. `TestPointerCLI` (`internal/app`, in `go test ./...`) runs the built binary against a headless NeferWL and checks a click, a drag, a scroll and the release at exit on a test client.

## wayvnc check

`make e2e-wayvnc` runs `TestWayVNC` (build tag `wayvnc`; skips without `wayvnc` on `PATH`). A headless NeferWL with a 320x240 output allowlists wayvnc in a throwaway capture-allow file and maps a client that shows a red square; then `wayvnc -o HEADLESS-1` runs on a free localhost port. A minimal RFB client in the test checks that the framebuffer has the output's size and shows the square, that a pointer event over the square enters the client and a click reaches it, and that a key event types `a` (KEY_A) into the focused client.

## X11 check

`make e2e-xwayland` runs the end-to-end test of the built-in window manager (`xwayland = builtin`). It is behind the `xwayland` build tag, so `go test ./...` and `make check` only compile it (`go vet -tags xwayland`). It needs `Xwayland` on `PATH` and a GPU, and skips without Xwayland. The clipboard test also needs `wl-copy` and `wl-paste` (wl-clipboard) and skips without them; they run with the headless `WAYLAND_DISPLAY` and need no extra focus step, since the focused probe window is enough for `wl-copy --foreground` to set the selection.

The test starts a headless NeferWL with a real `Xwayland` on the first free X display (a live session's `:0` stays untouched) and runs `internal/app/testdata/x11probe`, a small X11 client (`-class`, `-title`, `-size`, `-sleep`, `-at X,Y`) that paints its window green, prints `mapped WxH` and `focus`, and exits 0 on `WM_DELETE_WINDOW`. With `-override` it also maps a magenta override-redirect window (`-override-at`, `-override-size`, `-override-type menu|tooltip`, `-override-for`, `-override-shape`, `-override-dismiss-on-focusout`) and prints `override mapped`, `override unmapped` or `dismissed`. `-fullscreen` (with `-fullscreen-delay`, `-fullscreen-off`) requests `_NET_WM_STATE_FULLSCREEN` by client message and prints `fullscreen WxH` / `fullscreen off WxH` when the window manager publishes the state; `-keyboard-grab` calls `XGrabKeyboard` and prints `grab`. Windowless modes: `-copy TEXT|@file` (with `-copy-sel CLIPBOARD|PRIMARY`, `-copy-for`) owns a selection and serves `TARGETS`, `UTF8_STRING`, `STRING` and `TEXT`, with INCR above 64 KiB, and prints `copy owned`; `-paste CLIPBOARD|PRIMARY` prints `paste "<text>"`, `paste len=N sha256=H` above 4 KiB, or `paste none`; `-xsettings` prints `xsettings serial=N Xft/DPI=… Gdk/WindowScalingFactor=… Gdk/UnscaledDPI=… Gtk/CursorThemeName=… Gtk/CursorThemeSize=…` or `xsettings none`; `-root-cursor` prints `cursor WxH` from XFixes `GetCursorImage`. `-at` also sends a ConfigureRequest with the window rect before the map, like games do. It checks:

- the state lists one visible `X11Probe` window with the probe's pid, and the screenshot is mostly green inside its tile;
- the probe receives focus;
- a second probe tiles beside the first;
- `key Super+q` closes the focused probe within 2 seconds and the state drops to one window;
- an override-redirect menu is drawn magenta above the green toplevel at its X position, is not listed in the state, leaves focus on the toplevel and disappears from the screenshot once the probe unmaps it;
- a menu is dismissed when its owner gets `FocusOut` because another X11 window took the focus;
- a menu with an empty input shape is still drawn and still not a window;
- a fullscreen request 1.5 s after the map fills the output (all green, no gap) and `Super+Shift+f` leaves it; the client can also leave it itself; a request at the map is also honoured immediately;
- with the keyboard grab, `Super+q` does not close a fullscreen focused client and works again once it leaves fullscreen; on a windowed client it closes it;
- on two outputs, a window created at the second output's origin opens on `HEADLESS-2` and fullscreens there;
- clipboard: `wl-copy` text of 4 KiB and 1 MiB (INCR) is read by `x11probe -paste`, and an `x11probe -copy` of the same sizes is read byte-exact by `wl-paste`, for CLIPBOARD and PRIMARY; `wl-copy --clear` gives `paste none`;
- XSETTINGS: serial, `Xft/DPI=98304` and `Gdk/WindowScalingFactor=1` at any output scale (also with `output.HEADLESS-1.scale = 2`), and the cursor theme and size from `XCURSOR_THEME`/`XCURSOR_SIZE`; `xwayland.xsettings = off` leaves no owner;
- root cursor: `GetCursorImage` over the root is the theme arrow at the theme size (a throwaway theme in `XCURSOR_PATH`);
- a window rule `rule.games.app-id = steam_app_\d+` floats an X11 window of class `steam_app_413150`, and not another class.

Not covered headless: relative pointer lock (needs a real pointer device) and Xwayland output hotplug (`--size` is fixed); these are unit-tested in the `wayland` adapter.

Run it by hand with `go test -tags xwayland -race ./internal/app -run TestXwayland -count=1 -v`.

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
XDG_STATE_HOME="$T/state" NEFERWL_HINT_DEBUG=1 dbus-run-session -- bash -c '
  T=$1
  gdbus call --session --dest org.a11y.Bus --object-path /org/a11y/bus \
    --method org.freedesktop.DBus.Properties.Set org.a11y.Status IsEnabled "<true>"
  /usr/lib/at-spi2-registryd >"$T/registry.log" 2>&1 &
  ready=$("$T/neferwl" session new --id hints --dbus-session --config "$T/config" \
    --ttl 60s -- --no-xwayland)
  export WAYLAND_DISPLAY=$(printf "%s" "$ready" | jq -r .wayland)
  "$T/neferwl" -s hints shot --every-frame --dir "$T/shots"
  foot sh -c "printf https://example.org/a; exec sleep 60" &
  chromium --ozone-platform=wayland --force-renderer-accessibility \
    --user-data-dir="$T/chrome" "file://$T/page.html" >"$T/chromium.log" 2>&1 &
  "$T/neferwl" -s hints key Super+g --until "window-mapped app_id=chromium"
  read -r -p "Visible hint label: " label
  "$T/neferwl" -s hints type "$label"
  "$T/neferwl" -s hints shot
  "$T/neferwl" -s hints shot --stop
  "$T/neferwl" session rm hints
' bash "$T"
```

All clients and state queries must run inside that wrapper shell so they share
its session bus as well as its Wayland display. Logs are in
`$T/state/neferwl/sessions/hints/latest.log`.
Prepare `$T/page.html` with underlined local links before running (the shell example also needs `jq`). OCR detects
URL-like text without an underline and underlined link text; ordinary menu
labels, buttons, separators and box borders do not produce OCR targets. Arrange both windows
visibly; open a menu if possible.
Wait for the clients to map, run `neferwl -s hints key Super+g`, then use
`neferwl -s hints type <visible-label>` while the session is running.
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

## OCR URL opening

OCR targets with an http, https or mailto URL run `xdg-open` instead of a click. The recipe builds the binary, writes a minimal config, and puts a fake `xdg-open` first in `PATH`. It needs the hint prerequisites listed above.

```sh
T=$(mktemp -d); mkdir "$T/bin"
CGO_ENABLED=0 go build -o "$T/neferwl" ./cmd/neferwl
printf '#!/bin/sh\nfor a in "$@"; do echo "$a" >>%s/opened; done\n' "$T" >"$T/bin/xdg-open"
printf '#!/bin/sh\nprintf https://example.com; exec sleep 60\n' >"$T/show.sh"
chmod +x "$T/bin/xdg-open" "$T/show.sh"
cat >"$T/config" <<EOF
hint.enable = on
hint.capture = on
hint.sources = atspi, ocr
bind.cmd+g = hint
bind.cmd+u = spawn foot -o main.font=monospace:size=28 $T/show.sh
EOF
PATH="$T/bin:$PATH" "$T/neferwl" session new --id ocr --config "$T/config" --ttl 60s -- --no-xwayland
"$T/neferwl" -s ocr shot --every-frame --dir "$T/shots"
"$T/neferwl" -s ocr key Super+u --until 'window-mapped app_id=foot'
"$T/neferwl" -s ocr key Super+g
"$T/neferwl" -s ocr shot
# Read the visible hint label, then:
"$T/neferwl" -s ocr type '<label>'
"$T/neferwl" -s ocr shot --stop
"$T/neferwl" session rm ocr
```

The foot window shows `https://example.com`. Typing its hint label opens that
URL; `$T/opened` holds exactly `https://example.com`. A `file:` target is clicked
and leaves it empty. Launcher failures are in the session log under
`component=launcher`.

## Primary output coordinates

`output.<name>.primary = on` makes that output the origin for public
`wl_output.geometry` and `xdg_output.logical_position`. All public output
positions are translated by the same offset; without a configured primary,
positions stay unchanged. Layout or primary changes resend existing bindings,
with positions preceding their `done` event. Internal layout, pointer and window
coordinates do not move.

Other protocol coordinate spaces deliberately stay unchanged:

- Xwayland's private xdg-output uses the xspace top-left origin, not the public
  primary origin, so its root and RandR geometry stay compatible with X windows.
  X11 primary selection is separate: honoring the configured primary for X11
  clients would also require setting RandR's primary output; this translation
  does not implement that.
- wlr-output-management head positions remain absolute compositor coordinates:
  clients must be able to round-trip a configuration without moving outputs.
- Layer-shell configures carry output-local sizes, not global positions.
- ext-workspace coordinates are workspace indices, not output positions.

For a two-output check with a nonzero internal primary position, use
`--size 640x360,640x360` and this throwaway config:

```ini
output.HEADLESS-2.primary = on
output.HEADLESS-2.right-of = HEADLESS-1
```

On the isolated headless display, `wayland-info -i wl_output` should report
HEADLESS-1 at (-640,0) and HEADLESS-2 at (0,0).

## Control input device

Control input announces device ID `1` named `control`. Use
`output.tv.input = control` to test device-bound focus on a virtual output.
Control `move` coordinates stay global logical coordinates.

## Session paths

`$XDG_RUNTIME_DIR/neferwl/sessions/<id>/` holds `meta.json`, `control.sock`,
`state.json` and `shots/`. Logs live in
`$XDG_STATE_HOME/neferwl/sessions/<id>/`; the 20 most recent session log
directories are kept.
