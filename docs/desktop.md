# Desktop integration

How NeferWL runs programs and works with bars, clipboard managers, X11 apps, idle tools and scripts.

## Running commands

`spawn <command>` binds, `startup = <command>` lines and `terminal = <command>` run one program directly, without a shell. NeferWL splits the line on spaces: the first word is the program, found in `PATH` or given as a path such as `/opt/tool/run`, and the other words are its arguments.

```text
bind.ctrl+cmd+space = spawn fuzzel
startup = waybar
startup = wl-paste --watch cliphist store
```

Pipes (`|`), `&&`, redirections, variables (`$HOME`), `~` and quotes have no special meaning: they reach the program as plain arguments. For those, write a script and run it:

```sh
#!/bin/sh
# ~/.local/bin/screenshot-main: save the main monitor with a dated name.
nefercap screenshot -output DP-1 -file "$HOME/Pictures/$(date +%F-%T).png"
```

```text
bind.cmd+shift+p = spawn screenshot-main
```

Make the script executable (`chmod +x`) and put it in a directory of your `PATH`, such as `~/.local/bin`. It can use any shell, including fish (`#!/usr/bin/env fish`).

Programs started by NeferWL get `WAYLAND_DISPLAY`, `DISPLAY` (see [X11 apps](#x11-apps)) and `NEFERWL_STATE` (see below). `startup` commands run once when the session starts, so editing them takes effect at the next start.

## Clipboard

NeferWL supports the clipboard (`wl_data_device`), the primary selection (middle-click paste) and `ext_data_control_v1` for clipboard managers. To keep a history with [cliphist](https://github.com/sentriz/cliphist), store every copy at startup and bind a picker script:

```text
startup = wl-paste --watch cliphist store
bind.cmd+v = spawn cliphist-pick
```

`~/.local/bin/cliphist-pick`:

```sh
#!/bin/sh
cliphist list | fuzzel --dmenu | cliphist decode | wl-copy
```

Drag and drop is not supported yet.

## Input methods

NeferWL supports `zwp_text_input_v3` for apps and `zwp_input_method_v2` for one input method, such as [fcitx5](https://fcitx-im.org) or IBus. Start the input method at session start:

```text
startup = fcitx5 -d
```

Text input follows keyboard focus. The input method can grab the keyboard to compose text from keys (CJK, dead keys); the focused app then receives the composed text. A second input method gets `unavailable` until the first one exits. Candidate popups (`zwp_input_popup_surface_v2`) receive the text cursor position but are not drawn yet.

## X11 apps

X11 apps such as Steam run through [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, found in `PATH`. NeferWL opens an X11 display, sets `DISPLAY` for the programs it starts, and starts xwayland-satellite when the first X11 app connects. If xwayland-satellite exits, the next X11 app starts it again.

```text
xwayland = xwayland-satellite   # the default; a path also works
xwayland = off                  # no X11 display
```

Do not start xwayland-satellite yourself with `startup`. Changing `xwayland` takes effect at the next start.

## Session locking

External lockers use `ext-session-lock-v1`. NeferWL isolates desktop input and captures, covers every output, and stays protected if the locker dies. Authentication belongs to the locker; screen-off and a visual cover do not replace locking. See [Session locking](session-lock.md) for confirmation, recovery and security limits.

## Screen capture

nefercap, `grim`, recorders and screen sharing use `zwlr_screencopy_v1` and `ext_image_copy_capture_v1`. Every capture shows a red border, only the executables on the built-in list (`grim`, `nefercap`, `xdg-desktop-portal-wlr`) or in `/etc/neferwl/capture-allow`, which replaces it, may capture, and sandboxed clients use the portal. See [Screen capture](capture.md).

## Idle and screen off

NeferWL supports `ext_idle_notifier_v1` and `zwlr_output_power_management_v1`, so [swayidle](https://github.com/swaywm/swayidle) and [wlopm](https://git.sr.ht/~leon_plickat/wlopm) turn the screens off after a delay. A window that inhibits idle, such as a video player or a game, keeps them on. A display that disconnects in deep sleep comes back off when it reconnects, unless there was keyboard, pointer or touch input in the meantime.

Programs that inhibit idle over D-Bus instead (browsers, Electron apps, VLC) are covered too: NeferWL serves `org.freedesktop.ScreenSaver` on the session bus. The desktop portal's `Inhibit` reaches it only through a portal backend that forwards to that name, such as `xdg-desktop-portal-gtk`; `xdg-desktop-portal-wlr` does not implement `Inhibit`. An inhibition ends when the program releases it or leaves the bus, and `SimulateUserActivity` counts as input. If another program owns the name, or the session bus is missing, NeferWL logs a warning and tries again later, so it takes over once the name is free or the bus is back.

```text
startup = swayidle -w timeout 300 wlopm-off resume wlopm-on
```

`swayidle` runs its commands through a shell; NeferWL does not, so the quoted form `timeout 300 'wlopm --off "*"'` does not fit on a `startup` line. Put each command in a script (see [Running commands](#running-commands)):

```sh
#!/bin/sh
# ~/.local/bin/wlopm-off (wlopm-on is the same with --on)
exec wlopm --off '*'
```

## Bars

NeferWL supports `ext_workspace_manager_v1` for bars such as Waybar 0.13+ (`ext/workspaces`) and ironbar. Bars receive workspace updates and can switch workspaces without polling. Every workspace sends an `id`, the same string as its `workspace_id` in the state file. A configured (named) workspace has `name:<configured name>`, stable across launches; renaming it in the config removes its handle and sends a new one. A numbered or dynamic workspace has `<prefix>-<n>`: the prefix is 8 lowercase hex characters drawn once per compositor launch, so the `id` is unique per launch, stable while the workspace exists (across reordering and moves between outputs), and never repeats in the next launch. Numbered workspaces carry an `id` too, so a bar or script can target them during the session, but it is unique for the current launch only: do not store preferences keyed on it across launches. Only named workspaces have an `id` that is stable across launches.

NeferWL also supports `zwlr_foreign_toplevel_manager_v1`. Taskbars can list, focus, close and fullscreen windows, and notification daemons such as Dunst can detect fullscreen windows. The read-only `ext_foreign_toplevel_list_v1` lists the same windows with a stable `identifier` (`neferwl-<n>`, never reused in a session); screen-sharing portals use it to offer single windows. While unlocked, clients can see window titles and app IDs. Workspace and window inventory updates are withheld during session protection.

## State for scripts

While it runs, NeferWL writes its state to `$XDG_RUNTIME_DIR/neferwl/<wayland socket>.json` and passes that path to the programs it starts as `NEFERWL_STATE`. The file lists:

- every output, with its active numbered workspace, workspace count, and the name (`workspace`) and ID string (`workspace_id`) of the workspace on screen;
- the focused output and window;
- every window, with its app ID, PID, output, workspace, `workspace_id` (the ID string of its workspace, numbered or hidden) and whether it is on screen (`visible`);
- `floating: true` for floating windows. Stashed windows also have their 1-based place in their workspace's stash (`stash_index` of `stash_count`, both `0` outside it) and `hidden: true` while the stash is hidden.

```sh
neferwl state                        # the whole state as JSON
neferwl state output-of "$PID"       # the output of that process's window, or its nearest parent's
```

`output-of` finds application windows only, because a bar has no single output.

```sh
# Windows waiting in hidden stashes
neferwl state | jq '[.windows[] | select(.hidden)] | length'
```
