# Game server

`neferwl --backend=server` runs NeferWL without a display: it drives only the virtual outputs declared in the config, reads input devices directly and serves screen casting on the session bus. With [Sunshine](https://github.com/LizardByte/Sunshine), it turns a headless machine into a game streaming host.

Server mode differs from a desktop session:

- No DRM/KMS and no logind session: no physical output lights up, and no VT is taken.
- Virtual outputs are laid out left to right in config order, starting at 0,0. They take the startup focus and every pointer reaches them, so they are not isolated as on a desktop.
- The screen-casting and screensaver D-Bus services always run. The logind power-key lock does not.
- No terminal opens at startup. Scale changes are saved to the config, as on a desktop.
- The test flags (`--screenshot`, `--screenshot-raw`, `--input`, `--size`, `--timeout`, `--headless-hdr`, `--dbus`) are rejected.

## Requirements

- A GPU with a DRM render node (`/dev/dri/renderD*`). Software rasterizers such as lavapipe are refused.
- PipeWire, `xdg-desktop-portal` and `xdg-desktop-portal-gnome`, with the packaged `neferwl-portals.conf` (see [Portals](capture.md#portals)).
- Sunshine with portal capture.

## Account setup

The user service runs without anyone logged in:

```sh
sudo loginctl enable-linger $USER
```

Without a logind seat, NeferWL opens `/dev/input/event*` itself, so the user must be in the `input` group. On distributions where `/dev/dri/renderD*` is not world-accessible, add `render` too:

```sh
sudo usermod -aG input,render $USER
```

Log out and back in (or reboot) to apply.

**Security.** Membership in `input` lets every program of that user read every keyboard of the machine: it is the same as a keylogger. NeferWL grabs each device exclusively (`EVIOCGRAB`), so a local keyboard types into the server session, not into a console or another session. A device it cannot grab (another program already holds it) is skipped and logged as `input device skipped`. Use a dedicated account, and never run server mode while a desktop NeferWL session is running on the same machine: `neferwl-server.service` conflicts with `neferwl.service` for that reason.

## Configuration

`~/.config/neferwl/config`:

```ini
output.tv = virtual 1920x1080@60
output.tv.hdr = on
startup = steam -gamepadui
# Only when several GPUs are present, see Configuration > GPUs:
# render.device = /dev/dri/renderD128
```

Check it with `neferwl validate-config`. See [Virtual outputs](config.md#virtual-outputs) and [GPUs](config.md#gpus).

## Services

```sh
systemctl --user enable --now neferwl-server.service
systemctl --user enable --now sunshine.service
```

`neferwl-server.service` starts `neferwl --backend=server --session`, notifies systemd when ready, exports `WAYLAND_DISPLAY` and `DISPLAY` to the user manager and restarts on failure. It binds `graphical-session.target`, so Sunshine and the portals start in the right environment.

## Sunshine

`~/.config/sunshine/sunshine.conf`:

```ini
capture = portal
```

To match the client's resolution and refresh rate, change the virtual output's mode before each stream (`global_prep_cmd`, a JSON list):

```ini
global_prep_cmd = [{"do":"sh -c \"wlr-randr --output tv --custom-mode ${SUNSHINE_CLIENT_WIDTH}x${SUNSHINE_CLIENT_HEIGHT}@${SUNSHINE_CLIENT_FPS}\"","undo":"wlr-randr --output tv --custom-mode 1920x1080@60"}]
```

## First portal approval

The first time Sunshine captures, the portal shows its share dialog on `tv`, with nobody in front of the screen. Accept it once over SSH:

```sh
export $(systemctl --user show-environment | grep -E '^(WAYLAND_DISPLAY|XDG_RUNTIME_DIR)=')
grim -o tv /tmp/tv.png   # look at the dialog
ydotool key 28:1 28:0    # Return accepts the selected output
```

Move the selection with Tab (`15:1 15:0`) if needed, and take another screenshot to check. The portal gives Sunshine a restore token: later streams start without the dialog, as long as the output keeps its name.

## HDR

With `output.tv.hdr = on`, the output is HDR (PQ/BT.2020, reported as a 1000-nit display) and the cast offers 10-bit PQ first. Enable HDR in the Moonlight client and in the game. If the renderer cannot do HDR, the output falls back to SDR and the log says why. See [HDR](config.md#hdr) and [Screen capture](capture.md#portals).

## Input

Moonlight's keyboard, mouse and gamepads reach the host through Sunshine's virtual devices. Keyboard and mouse events go through NeferWL to the focused window. Gamepads go straight to games: NeferWL does not handle them.

Steam's controller rules grant access through `uaccess`, which needs a logind seat. Server mode has none, so the `input` group is what gives Steam its gamepads.

## Controlling a NeferWL session (agents)

An agent or script on the host can watch the session and drive it with standard Wayland tools. Run them as the session user with the session's display:

```sh
export $(systemctl --user show-environment | grep -E '^(WAYLAND_DISPLAY|XDG_RUNTIME_DIR)=')
grim -o tv /tmp/screen.png        # capture one output (see Screen capture)
wlrctl pointer move 200 -50       # relative motion, in logical pixels
wlrctl pointer click left         # click where the pointer is
wtype 'hello'                     # type into the focused window
neferwl state                     # windows, focus, outputs as JSON
```

- The pointer tools use `zwlr_virtual_pointer_v1`. NeferWL routes their events like a real mouse, so focus, hover and drags behave as they would for a person. Absolute motion maps into the whole layout, or into one output when the tool binds it (`create_virtual_pointer_with_output`).
- `ydotool` writes to `/dev/uinput` through `ydotoold` and does not use this protocol; prefer `wlrctl`.
- Nothing is injected while the session is locked. Buttons a tool still held when it disconnects are released.
- Flatpak and other sandboxed clients (those on a `wp_security_context_v1` socket) do not see the virtual pointer global.
- `grim` needs the capture allowlist to admit it; see [the allowlist](capture.md#the-allowlist).

[wayvnc](https://github.com/any1/wayvnc) works for remote viewing and control: it captures an output with wlr-screencopy and injects input through the virtual pointer and keyboard. Add its executable (`/usr/bin/wayvnc`) to `/etc/neferwl/capture-allow`, then run it in the session:

```sh
wayvnc -o tv 127.0.0.1 5900
```

Keep it on localhost and reach it through an SSH tunnel, or configure wayvnc's own authentication and TLS.

## Hardening

On a dedicated box, a local keyboard should not reboot or reach a console:

```sh
sudo systemctl mask ctrl-alt-del.target
echo kernel.sysrq=0 | sudo tee /etc/sysctl.d/90-sysrq.conf
sudo systemctl disable --now getty@tty1.service
```

## Limits

- Touch screens and pens (tablets) are not supported.
- Only virtual outputs exist: a connected monitor stays dark.
