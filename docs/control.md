# Scripting and agents

Every headless NeferWL session has a control socket. Scripts, tests and AI
agents use it to run actions, inject input, read state and take screenshots,
and each command waits for its result, so no fixed sleeps are needed. Use it on
a throwaway session to test a config or an app away from your desktop, or on
the long-running [server](server.md). The `neferwl` CLI wraps the socket; the
wire protocol is in [docs/dev/control.md](dev/control.md).

## Sessions

A session is one headless compositor with its own Wayland display, control
socket, state and captures.

```sh
neferwl session new --id demo --ttl 10m     # start a session, print its readiness JSON
neferwl session ls                          # list live sessions
neferwl session rm demo                     # stop it
```

`session new` starts the compositor detached and prints one JSON line once it
is ready:

```json
{"id":"demo","wayland":"wayland-3","control":"/run/user/1000/neferwl/sessions/demo/control.sock","display":":2"}
```

`display` is the X11 display, empty with `--no-xwayland`. `--env` prints
`NEFERWL_SESSION=` and `WAYLAND_DISPLAY=` lines instead.

| Flag | Default | Meaning |
|---|---|---|
| `--id ID` | random | Session ID |
| `--profile` | `ephemeral` | `ephemeral` or `persistent`; see [Headless and server mode](server.md#profiles) |
| `--size WxH[,WxH...]` | `1920x1080` (ephemeral) | Output sizes, laid out left to right (`HEADLESS-1`, `HEADLESS-2`, ...) |
| `--config PATH` | XDG default | Config file; use a throwaway one for tests |
| `--ttl DURATION` | none | The session exits after this time |
| `--start-timeout DURATION` | `30s` | Give up if the session is not ready in time |
| `--dbus-session` | off | Keep the parent D-Bus session bus (removed by default) |
| `--env` | off | Print shell assignments instead of JSON |

Compositor flags go after `--`, for example
`neferwl session new --id demo -- --no-xwayland --no-terminal`. The ephemeral
profile opens the configured terminal at startup unless `--no-terminal` is
given.

The session does not inherit the caller's `WAYLAND_DISPLAY`, `DISPLAY` or
session targets, so apps it starts never open on your desktop. Logs go to
`$XDG_STATE_HOME/neferwl/sessions/<id>/latest.log`.

`neferwl-server.service` runs the persistent [server](server.md) as the session
`server`.

## Targeting

Control commands pick their session in this order:

1. `-s ID`;
2. `NEFERWL_SESSION`;
3. `NEFERWL_CONTROL` (a control socket path);
4. the session whose Wayland display is `WAYLAND_DISPLAY`.

Programs started inside a session get `NEFERWL_SESSION`, `NEFERWL_CONTROL` and
`WAYLAND_DISPLAY`, so `neferwl key` run from a session's terminal controls that
session. In scripts, always pass `-s ID`. Without a target, the command fails
and lists live sessions.

Start your own clients with the session's `wayland` display
(`WAYLAND_DISPLAY=wayland-3 foot &`), or let the compositor start them with
`neferwl -s demo do spawn foot`.

## Act, then observe

Each action or input command waits for its effect, then replies with what
changed:

```sh
neferwl -s demo do spawn foot --until 'window-mapped app_id=foot'
```

```json
{"ok":true,"done":"settled","diff":{"mapped":[2],"focus_from":1,"focus_to":2,"focus_changed":true},...}
```

| Command | Effect |
|---|---|
| `do ACTION [ARG...]` | Run a bind action: `do focus-workspace-next`, `do spawn foot`, `do workspace dev` |
| `key COMBO` | Press and release a combination: `key Super+Return` |
| `type TEXT` | Type text with the session's keymap |
| `press COMBO`, `release COMBO` | Hold or release keys |
| `move X Y` | Move the pointer to global logical coordinates |
| `click [left\|right\|middle]` | Click a button (left by default) |
| `swipe 3\|4 up\|down\|left\|right` | Touchpad swipe |

Action names are the bind actions of the [configuration](config.md). Compositor
binds such as `Super+o` also work through `key`. These flags go before or after
the arguments:

| Flag | Default | Meaning |
|---|---|---|
| `--wait LEVEL` | `settled` | `committed`: the action is applied. `settled`: animations are done and windows have committed their new sizes. `presented`: the result is on screen. |
| `--until PREDICATE` | none | Also wait for `window-mapped app_id=APP`, `focused app_id=APP` or `unmapped id=N` |
| `--timeout DURATION` | `10s` | Wait at most this long, up to `5m` |
| `--shot` | off | Capture every output once the result is presented |

`diff` lists mapped and unmapped window IDs, the focus change and the workspace
now shown on each changed output (`HEADLESS-1:2`). On timeout the command
prints `timeout` and exits with status 1. The reply still carries the partial
diff; the action may have run, so check the state before retrying.

`--` ends flag parsing for arguments that start with a dash:
`neferwl -s demo move -- -640 0`, `neferwl -s demo type -- -x`.

There is no sleep or hold command: use the shell between `press` and `release`.

## Observe state

```sh
neferwl -s demo state                      # outputs, workspaces, windows, focus
neferwl -s demo state | jq '.windows[] | {id, app_id, workspace, visible}'
neferwl -s demo query state                # the same state inside a reply envelope
```

The state has the shape of the desktop [state file](desktop.md#state-for-scripts)
and never contains window titles.

## Captures

```sh
neferwl -s demo shot                       # every output
neferwl -s demo shot --output HEADLESS-1   # repeat --output for several
neferwl -s demo shot --inline              # also return base64 PNG data
```

A shot waits for the next presented frame and writes one 8-bit sRGB PNG per
output under `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/shots/`. The reply lists
`{output, path, seq}` for each, where `seq` is the presented scene. `--shot` on
an action captures its result in the same round trip. Single shots do not show
the cursor.

### Every frame and video

```sh
neferwl -s demo shot --every-frame                   # start writing frames
neferwl -s demo swipe 4 down --wait presented
neferwl -s demo shot --stop                          # stop and close the index
frames="$XDG_RUNTIME_DIR/neferwl/sessions/demo/shots/frames"
ffmpeg -f concat -safe 0 -i "$frames/frames.ffconcat" -vf "fps=60,format=yuv420p" -c:v libx264 -crf 18 demo.mp4
```

Continuous capture writes `frame-NNNNNN.png` per presented frame, `latest.png`
(a hard link to the newest frame) and `frames.ffconcat` with the frame timing.
`--dir DIR` selects another directory and `--output NAME` limits capture to
that output. With several outputs, each gets a subdirectory named after it.
The directory grows until `shot --stop`.

On an HDR output, `--every-frame --raw` also writes `latest-pq.png`, the newest
frame in raw PQ.

## Runtime outputs

```sh
neferwl -s demo output add 1280x720@60 --name side
neferwl -s demo output rm side
```

An added output lasts until `output rm` or the end of the session. The reply
comes once the state lists or drops it.

## Security

- The socket and its directory are private to the user (`0600`, `0700`), and
  the compositor checks the peer's uid with `SO_PEERCRED`. Any process of the
  same user gets full control, including input and captures; other users are
  rejected.
- While the session is locked (`ext-session-lock-v1`), actions, input and
  captures are refused with `locked`, and continuous capture writes no frames.
  `quit` is still accepted so `session rm` can stop a locked session. Same-uid
  access does not bypass the lock.

## Limits

- Only headless sessions have a control socket. On a DRM desktop, use the
  [state file](desktop.md#state-for-scripts) and Wayland tools.
- Single shots are 8-bit sRGB; raw PQ is only available with `--every-frame`.
- On a device without sync-file export (software Vulkan such as lavapipe, in
  CI or VMs), shots are read back on the CPU: slower, same pixels.
- At most 64 requests wait at once per session; more get `busy`.
