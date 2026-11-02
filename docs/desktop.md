# Desktop integration

## Running commands

`spawn` binds, `startup` and `terminal` run a program directly, **without a shell**. The line is split on spaces: the first word is the program (from `PATH` or a full path), the rest are its arguments. `;` separates several commands, which all start at once.

```text
bind.ctrl+cmd+space = spawn fuzzel
startup = waybar; mako; wl-paste --watch cliphist store
```

Pipes, `&&`, redirections, `$VARIABLES`, `~` and quotes are passed as plain text. For those, write a script in your `PATH` and run it:

```sh
#!/bin/sh
# ~/.local/bin/screenshot-main
nefercap screenshot -output DP-1 -file "$HOME/Pictures/$(date +%F-%T).png"
```

```text
bind.cmd+shift+p = spawn screenshot-main
```

Programs started by NeferWL get `WAYLAND_DISPLAY`, `DISPLAY` and `NEFERWL_STATE`. `startup` changes apply at the next session.

## Clipboard

The clipboard, primary selection (middle-click paste) and clipboard managers work. A [cliphist](https://github.com/sentriz/cliphist) history:

```text
startup = wl-paste --watch cliphist store
bind.cmd+v = spawn cliphist-pick
```

```sh
#!/bin/sh
# ~/.local/bin/cliphist-pick
cliphist list | fuzzel --dmenu | cliphist decode | wl-copy
```

Drag and drop works between clients: the surface under the pointer gets the offer, the drop and the chosen action (copy, move or ask, from the modifiers the clients read), and the drag icon follows the cursor. Escape, a refused drop, a dying source or the session lock cancel the drag.

## Input methods

[fcitx5](https://fcitx-im.org) and IBus work through `zwp_input_method_v2`. Start one at session start:

```text
startup = fcitx5 -d
```

Candidate popups are not drawn yet.

## X11 apps

By default, X11 apps such as Steam run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7+, found in `PATH`. NeferWL starts it when the first X11 app connects; do not start it yourself.

```text
xwayland = off   # no X11 support
```

With `xwayland = builtin` (experimental), neferwl starts `Xwayland` itself and manages X11 windows: they tile, take focus and close like Wayland windows. Menus, popups and tooltips of X11 apps are shown above their window and do not take the keyboard focus. X11 apps get the cursor theme (`XCURSOR_THEME`), cursor size (`XCURSOR_SIZE`) and DPI through XSETTINGS, always announced as scale 1 (96 dpi) because X11 apps see logical sizes and neferwl upscales their windows (`Gtk/CursorThemeName`, `Gtk/CursorThemeSize`, `Xft/DPI`, `Gdk/WindowScalingFactor`, `Gdk/UnscaledDPI`; `xwayland.xsettings = off` to disable), and the X background shows the themed arrow. The clipboard and the primary selection work between X11 and Wayland apps in both directions, for text; large copies are transferred in chunks (INCR). While the session is locked, no selection data crosses.

Games with `builtin`:

- **Fullscreen:** an X11 window can ask for fullscreen (`_NET_WM_STATE_FULLSCREEN`) and leave it again; `toggle-fullscreen` works too. A window that maps with the fullscreen state already set (games do) opens fullscreen in a column of its own, focused, even if its size is fixed. Requests after map are honoured immediately, subject to the usual fullscreen focus rules. A new window of the same app comes forward without clearing fullscreen; windows of other apps wait hidden. See the [same-app fullscreen policy](config.md#layout).
- **Output:** a window that opens exactly at one output's area opens on that output, and its fullscreen fills it.
- **Keyboard grab:** an X11 app that grabs the keyboard inhibits the compositor binds only while it is focused and fullscreen; a windowed app cannot lock the binds. There is no escape bind, as with Wayland shortcut inhibitors; leave by unfullscreening from the app or switching away with the mouse.
- **Pointer:** mouse-look works through relative pointer and pointer constraints.
- **App ID:** Proton windows are named `steam_app_<id>`; match them with a [window rule](config.md#window-rules).
- **Bypass hint:** `_NET_WM_BYPASS_COMPOSITOR` is read but changes nothing; direct scanout is chosen from the window state.

## Session locking

NeferWL supports `ext-session-lock-v1` with an external locker such as swaylock or hyprlock. The locker checks the password; NeferWL hides the desktop and blocks input.

### While locked

- Only the locker gets input. Binds, clipboard requests and new captures are refused.
- Outputs show black and the locker's surfaces only.
- Running captures stop.
- `Ctrl+Alt+Backspace` is disabled. VT switching still works.
- A locker that uses a screenshot as background (hyprlock `screenshot`, swaylock-effects `--screenshots`) must be in the [capture allowlist](capture.md#the-allowlist).

The locker is told `locked` only once every output shows a protected frame or is off.

### If the locker dies

The session stays locked and black. Start a new locker to take over. There is no emergency unlock key and no timeout.

A locker that exits right after unlocking must do a Wayland roundtrip first, or its exit counts as a crash and the session stays locked.

### Limits

- NeferWL does not authenticate: no PAM, no stored credentials.
- Any process running as your user can start its own locker and unlock.
- Clipboard data and window contents a client received before the lock stay with that client.
- The [state file](desktop.md#state-for-scripts) is not cleared while locked.

## Screen capture

nefercap, `grim` and screen sharing through `xdg-desktop-portal-gnome` work out of the box. Other tools must be added to the allowlist. See [Screen capture](capture.md).

## Idle and screen off

[swayidle](https://github.com/swaywm/swayidle) and [wlopm](https://git.sr.ht/~leon_plickat/wlopm) turn the screens off after a delay:

```text
startup = swayidle -w timeout 300 wlopm-off resume wlopm-on
```

```sh
#!/bin/sh
# ~/.local/bin/wlopm-off (wlopm-on is the same with --on)
exec wlopm --off '*'
```

Video players, games and browsers keep the screens on while they ask to, through Wayland or D-Bus (`org.freedesktop.ScreenSaver`).

## Power keys

NeferWL stops logind from acting on the power, suspend, hibernate and reboot keys. They do nothing unless you bind them:

```text
bind.XF86PowerOff = spawn systemctl suspend
bind.XF86Suspend = spawn systemctl hibernate
```

| Key | Bind name |
|---|---|
| Power | `XF86PowerOff` |
| Sleep | `XF86Sleep` |
| Suspend | `XF86Suspend` |
| Reboot | `code:408` |

The lid switch is still handled by logind.

## Bars

Waybar 0.13+ (`ext/workspaces`) and ironbar show and switch workspaces. Taskbars can list, focus and close windows.

Workspace IDs are `name:<name>` for named workspaces, stable across launches. Numbered workspaces get a random prefix per launch, so do not save settings keyed on them.

## State for scripts

NeferWL writes its state as JSON to `$XDG_RUNTIME_DIR/neferwl/<wayland socket>.json`, also given to started programs as `$NEFERWL_STATE`. It lists outputs, workspaces, the focused window, and every window with its app ID, PID, workspace, column and row, and whether it is floating or the fullscreen window of its workspace (`floating`, `fullscreen`). A fullscreen window keeps `fullscreen` while a stashed window is shown over it and while its workspace is not shown; combine it with `visible` to know what covers an output.

Each window also carries metadata keys:

- `idle_inhibit`: the window keeps the session from idling.
- `shortcuts_inhibited`: the window asks to receive compositor shortcuts; they go to it only while it has keyboard focus.
- `pointer_constraint`: `locked`, `confined` or empty.
- `sandbox`: the app ID the sandbox engine declared through `wp_security_context_v1`, else the engine name; empty when the window is not sandboxed. The sandbox declares it; NeferWL does not verify it.
- `content_type`: `game`, `video`, `photo` or empty, from `wp_content_type_v1`.
- `xwayland`: the window is an X11 window served through xwayland-satellite.

The `workspaces` list holds every workspace with its `visible` flag, its columns and the windows in them, each with `x`, `y`, `width` and `height` in logical pixels of its output. The file is written at most once per second.

```sh
neferwl state                        # the whole state
neferwl state output-of "$PID"       # the output showing that process's window
neferwl state | jq '[.windows[] | select(.hidden)] | length'   # windows in hidden stashes
neferwl state | jq '[.windows[] | select(.fullscreen)] | length'   # fullscreen windows
neferwl state | jq '.workspaces[] | select(.visible) | .columns[] | .width'   # column widths on screen
```

`neferwl state` includes an `hdr` block per output: `tier` (unavailable,
assumed, full), `peak_nits`, `peak_source` (config, edid, default), `headroom`,
`active` (confirmed HDR10 signaling), `sdr_expand` (off, auto, always), and
`expandable` (capability headroom meets the automatic threshold, independent
of active mode). Each window has `sdr_expansion.active` and `reason`; active
expansion also includes `intensity`, `top` (SDR-white units), and `decode`.

```sh
neferwl state | jq '.outputs[] | {name, hdr}'
neferwl state | jq '.windows[] | {app_id, sdr_expansion}'
```
