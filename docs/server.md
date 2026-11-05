# Headless and server mode

`neferwl --backend=headless` runs the compositor without a screen. Apps connect
to its Wayland display as usual and draw into virtual outputs, which you can
stream, record or capture. Use it for a game streaming host, a remote desktop
box, demo recordings or tests in CI.

## Profiles

| | `ephemeral` (default) | `persistent` |
|---|---|---|
| Use | Throwaway sessions: tests, scripts, agents, recordings | A long-running server |
| Outputs | `--size` heads (`HEADLESS-1`, default 1920x1080); config virtual outputs stay isolated | Virtual outputs from the config |
| Input devices | None; input comes from the [control socket](control.md) | Reads `/dev/input/event*` directly |
| `--input-devices` | `grab` reads devices too | `none` reads none |
| Screen casting and screensaver D-Bus services | Only with `--dbus` | Always |
| Scale changes | Not saved | Saved to the config |
| Terminal at startup | Opens | None |
| GPU | Any Vulkan device | A DRM render node; software rasterizers are refused |

Both profiles have no DRM/KMS and no logind session: no physical output lights
up, no VT is taken and the logind power keys are not handled. Both register a
session with a control socket, so [`neferwl -s ID ...`](control.md) works on
either. A session ends on `neferwl session rm ID`, on SIGINT or SIGTERM, on the
`quit` action, or when `--timeout` runs out (`--ttl` for `session new`).

In the persistent profile, virtual outputs are laid out left to right in config
order from 0,0. They take the startup focus and every pointer reaches them,
unlike virtual outputs on a desktop. `--size` also creates fixed `HEADLESS-N`
heads.

`--backend=server` is a deprecated alias for
`--backend=headless --profile=persistent`.

## Run it

Declare at least one virtual output in `~/.config/neferwl/config`:

```ini
output.tv = virtual 1920x1080@60
startup = foot
```

See [Virtual outputs](config.md#virtual-outputs) for modes, HDR and placement.
Then start the compositor by hand, for example over SSH:

```sh
neferwl --backend=headless --profile=persistent --session-id=box
```

or as the systemd user service:

```sh
systemctl --user enable --now neferwl-server.service
```

The service runs `neferwl --backend=headless --profile=persistent --session --session-id=server`.
It notifies systemd when ready, exports `WAYLAND_DISPLAY` and `DISPLAY` to the
user manager, binds `graphical-session.target` and restarts on failure. It
conflicts with `neferwl.service`, so it never runs beside a desktop session.

Programs started by the compositor (`startup`, binds, `do spawn`) already have
the right environment. To start a client yourself, take the display from the
session list or from the user manager:

```sh
neferwl session ls                                      # WAYLAND column
export $(systemctl --user show-environment | grep -E '^(WAYLAND_DISPLAY|DISPLAY|XDG_RUNTIME_DIR)=')
firefox &
```

Logs are in `$XDG_STATE_HOME/neferwl/sessions/<id>/latest.log` with `--session-id`, else in `$XDG_STATE_HOME/neferwl/runs/headless/latest.log`.

## Watch and control it

- **Streaming and recording**: the screen-casting portal shares each virtual
  output with [Sunshine](#game-streaming-with-sunshine), OBS or any PipeWire
  client. It needs PipeWire, `xdg-desktop-portal` and
  `xdg-desktop-portal-gnome` with the packaged `neferwl-portals.conf`; see
  [Portals](capture.md#portals).
- **Screenshots**: `neferwl -s server shot --output tv` writes a PNG and prints
  its path. `grim -o tv` works too once its executable is in
  [the allowlist](capture.md#the-allowlist).
- **Remote desktop**: [wayvnc](#wayvnc) gives a VNC view with keyboard and
  pointer.
- **Scripts and agents**: actions, input, state and captures through the
  control socket; see [Scripting and agents](control.md).

```sh
neferwl -s server state | jq '.windows[] | {id, app_id}'
neferwl -s server do focus-workspace-next
neferwl -s server key Super+Return --until 'window-mapped app_id=foot'
neferwl -s server shot --output tv
```

### Virtual pointer

Tools using `zwlr_virtual_pointer_v1` move the pointer like a real mouse, so
focus, hover and drags behave as they would for a person. Run them with the
session's `WAYLAND_DISPLAY`:

```sh
neferwl pointer abs 0.5 0.5 click
neferwl pointer move 200 -50 click right
```

- `neferwl pointer` actions: `abs X Y` (fractions 0..1 of the layout, or of one output with `--output <name>`), `move DX DY`, `press`/`release`/`click [BTN]`, `hold [BTN] [DURATION]`, `drag DX DY [BTN]` and `scroll up|down|left|right [N]`. BTN is `left` (default), `right`, `middle`, `side`, `extra`, `forward`, `back`, a `BTN_*` name or an evdev code. `--click-duration` (20ms), `--hold-duration` (500ms), `--drag-steps` (10) and `--drag-step-delay` (10ms) tune the timing. Buttons still pressed at exit are released. See `neferwl pointer --help`.
- [wlrctl](https://git.sr.ht/~brocellous/wlrctl) (AUR: `wlrctl`) works too: `wlrctl pointer move 200 -50`, `wlrctl pointer click left`.
- Absolute motion maps into the whole layout, or into one output when the tool binds it (`create_virtual_pointer_with_output`).
- `ydotool` writes to `/dev/uinput` through `ydotoold` and does not use this protocol; prefer `neferwl pointer`.
- Nothing is injected while the session is locked. Buttons a tool still held when it disconnects are released.
- Flatpak and other sandboxed clients (those on a `wp_security_context_v1` socket) do not see the virtual pointer global.

### wayvnc

[wayvnc](https://github.com/any1/wayvnc) captures an output with wlr-screencopy and injects input through the virtual pointer and keyboard. Add its executable (`/usr/bin/wayvnc`) to `/etc/neferwl/capture-allow`, then run it in the session:

```sh
wayvnc -o tv 127.0.0.1 5900
```

Keep it on localhost and reach it through an SSH tunnel, or configure wayvnc's own authentication and TLS.

## Game streaming with Sunshine

With [Sunshine](https://github.com/LizardByte/Sunshine), a persistent session
turns a machine without a screen into a game streaming host for Moonlight.

### Requirements

- A GPU with a DRM render node (`/dev/dri/renderD*`).
- PipeWire, `xdg-desktop-portal` and `xdg-desktop-portal-gnome`, with the packaged `neferwl-portals.conf` (see [Portals](capture.md#portals)).
- Sunshine with portal capture.

### Account setup

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

### Configuration

`~/.config/neferwl/config`:

```ini
output.tv = virtual 1920x1080@60
output.tv.hdr = on
startup = steam -gamepadui
# Only when several GPUs are present, see Configuration > GPUs:
# render.device = /dev/dri/renderD128
```

Check it with `neferwl validate-config`. See [Virtual outputs](config.md#virtual-outputs) and [GPUs](config.md#gpus).

### Services

```sh
systemctl --user enable --now neferwl-server.service
systemctl --user enable --now sunshine.service
```

`neferwl-server.service` binds `graphical-session.target`, so Sunshine and the portals start in the right environment.

`~/.config/sunshine/sunshine.conf`:

```ini
capture = portal
```

To match the client's resolution and refresh rate, change the virtual output's mode before each stream (`global_prep_cmd`, a JSON list):

```ini
global_prep_cmd = [{"do":"sh -c \"wlr-randr --output tv --custom-mode ${SUNSHINE_CLIENT_WIDTH}x${SUNSHINE_CLIENT_HEIGHT}@${SUNSHINE_CLIENT_FPS}\"","undo":"wlr-randr --output tv --custom-mode 1920x1080@60"}]
```

### First portal approval

The first time Sunshine captures, the portal shows its share dialog on `tv`, with nobody in front of the screen. Accept it once over SSH:

```sh
export $(systemctl --user show-environment | grep -E '^(WAYLAND_DISPLAY|XDG_RUNTIME_DIR)=')
grim -o tv /tmp/tv.png   # look at the dialog
ydotool key 28:1 28:0    # Return accepts the selected output
```

Move the selection with Tab (`15:1 15:0`) if needed, and take another screenshot to check. The portal gives Sunshine a restore token: later streams start without the dialog, as long as the output keeps its name.

### HDR

With `output.tv.hdr = on`, the output is HDR (PQ/BT.2020, reported as a 1000-nit display) and the cast offers 10-bit PQ first. Enable HDR in the Moonlight client and in the game. If the renderer cannot do HDR, the output falls back to SDR and the log says why. See [HDR](config.md#hdr) and [Screen capture](capture.md#portals).

### Input

Moonlight's keyboard, mouse and gamepads reach the host through Sunshine's virtual devices. Keyboard and mouse events go through NeferWL to the focused window. Gamepads go straight to games: NeferWL does not handle them.

Steam's controller rules grant access through `uaccess`, which needs a logind seat. Server mode has none, so the `input` group is what gives Steam its gamepads.

### Hardening

On a dedicated box, a local keyboard should not reboot or reach a console:

```sh
sudo systemctl mask ctrl-alt-del.target
echo kernel.sysrq=0 | sudo tee /etc/sysctl.d/90-sysrq.conf
sudo systemctl disable --now getty@tty1.service
```

## Limits

- Touch screens and pens (tablets) are not supported.
- Only virtual outputs exist: a connected monitor stays dark.
