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

X11 apps such as Steam run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7+, found in `PATH`. NeferWL starts it when the first X11 app connects; do not start it yourself.

```text
xwayland = off   # no X11 support
```

## Session locking

Use an external locker such as swaylock or hyprlock. See [Session locking](session-lock.md).

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

NeferWL writes its state as JSON to `$XDG_RUNTIME_DIR/neferwl/<wayland socket>.json`, also given to started programs as `$NEFERWL_STATE`. It lists outputs, workspaces, the focused window, and every window with its app ID, PID, workspace, column and row.

```sh
neferwl state                        # the whole state
neferwl state output-of "$PID"       # the output showing that process's window
neferwl state | jq '[.windows[] | select(.hidden)] | length'   # windows in hidden stashes
```
