# Configuration

NeferWL reads `~/.config/neferwl/config` (or `$XDG_CONFIG_HOME/neferwl/config`). Changes apply when you save, except `startup` and `xwayland`, which apply at the next start.

```sh
neferwl --config <path>            # use another file
neferwl validate-config [path]     # check a file
```

## Format

- One `key = value` per line. `#` starts a comment.
- Lists are comma-separated; booleans are `on` / `off`.
- An invalid line logs a warning and keeps the default. If a key appears twice, the last one wins.

See [examples/config](../examples/config) for every key with its default.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `keyboard.layout`, `.variant`, `.options` | empty | xkb names, e.g. `fr`, `caps:escape` |
| `keyboard.repeat-rate` / `.repeat-delay` | `25` / `600` | Repeats per second / delay in ms |
| `keyboard.cmd` | `super` | Modifier meant by `cmd` in binds: `super`, `alt` or `ctrl` |
| `terminal` | `$TERMINAL` or `foot` | Terminal command |
| `terminal.auto-open` | `first` | Open a terminal on the first workspace (`first`), on every empty one (`all`), or never (`off`) |
| `startup` | none | Commands run at session start, separated by `;` |
| `xwayland` | `xwayland-satellite` | X11 support; `off` disables it |
| `background` | `#111111` | Background color |
| `border.width` | `2` | Border width in pixels; `0` hides borders |
| `border.active` / `.inactive` | `#808080` / `#111111` | Border colors of the focused / other windows |
| `floating.dim` | `0.3` | Darkening behind floating windows and the overview, 0 to 1 |
| `layout.gaps` | `0` | Space between windows |
| `layout.max-columns` | `2` | Columns that share the screen |
| `layout.overflow` | `cascade` | What happens past `max-columns`: [`cascade`](#cascade) into bands below, `scroll` right, or `fixed` spiral split |
| `layout.<output>.max-columns`, `.overflow` | layout values | Per-output layout |
| `stash.width` | `80` | Width of the selected [stashed](#stash) window, in % of the screen (10–90) |
| `stash.gap` | `2` | Gap around the selected stashed window, in % (0–10) |
| `stash.dim` | `0.5` | Darkening of the stash neighbors, 0 to 1 |
| `stash.capture` | `on` | A window opened while the stash shows joins the stash |
| `stash.spawn-terminal` | `on` | `toggle-stash-visible` on an empty stash opens `terminal` in it |
| `touchpad.natural-scroll` | `off` | Content follows the fingers |
| `touchpad.tap` | `on` | Tap to click |
| `touchpad.accel-speed` | `0` | Pointer speed, `-1` to `1` |
| `touchpad.accel-profile` | `adaptive` | `adaptive` or `flat` |
| `touchpad.left-handed` | `off` | Swap left and right buttons |
| `touchpad.scroll-factor` | `1` | Scroll speed multiplier, up to `10` |
| `mouse.natural-scroll`, `.accel-speed`, `.accel-profile`, `.left-handed` | as touchpad | Same, for mice, trackballs and trackpoints |
| `cursor.hide-after` | `5s` | Hide the cursor after this long without motion, or `off` |
| `animations` | `off` | **Experimental.** Animate layout changes and make swipes follow the fingers |
| `animations.speed` | `normal` | `slow`, `normal`, `fast`, or a factor from `0.1` to `10` |
| `focus.follow-move` | `off` | Follow a column moved to another workspace |
| `focus.animation` | `pulse` | Brief brightening of a newly focused window, or `off`. Needs `animations = on` |
| `focus.strength` | `0.04` | Strength of the focus pulse, `0.01` to `0.2` |
| `workspace.<name>.monitor` | focused output | Home monitor of a named workspace |
| `workspace.<name>.size` | `inherit` | [Smaller viewport](#workspace-size), e.g. `1920x1080` |
| `workspace.<name>.max-columns`, `.overflow` | output values | Per-workspace layout |
| `workspace.<name>.column.<N>` | none | Column slot: `<cells>, <command>` |
| `rule.<name>.*` | none | [Window rules](#window-rules) |
| `output.<name>` | `preferred` | `WxH`, `WxH@Hz`, `preferred` or `off` |
| `output.<name>.scale` | `1` | 1 to 4, e.g. `1.5` or `4/3` |
| `output.<name>.transform` | `normal` | `90`, `180`, `270`, `flipped`, `flipped-90`, … |
| `output.<name>.primary` | `off` | Focused at startup |
| `output.<name>.right-of`, `.left-of`, `.above`, `.below`, `.offset` | automatic | [Output placement](#output-placement) |
| `output.<name>.hdr` | `off` | [HDR](#hdr) on capable displays |
| `output.<name>.sdr-brightness` | `203` | Brightness of SDR content in HDR, in nits (80–1000) |
| `render.direct-scanout` | `on` | Send fullscreen windows straight to the display |
| `render.tearing` | `on` | Allow tearing for fullscreen games |
| `render.vrr` | `on` | Variable refresh rate for fullscreen windows |
| `render.vrr-flip-gap` | `1ms` | See [Performance](performance.md#vrr) |
| `performance.realtime` | `on` | Real-time scheduling |
| `log.level` / `log.debug` | `info` / empty | Log level / debug categories or `all` |
| `bind.<keys>` | see [Default binds](#default-binds) | [Binds](#binds) |

`<output>` is a connector such as `DP-2`, or a monitor key (`make model serial`).

## Window rules

Rules place an app's windows when they first open.

| Key | Meaning |
|---|---|
| `rule.<name>.app-id` | Required. [Regular expression](https://pkg.go.dev/regexp/syntax) matching the whole app ID |
| `rule.<name>.floating` | `on` to float, `off` to tile |
| `rule.<name>.workspace` | Workspace number or name |
| `rule.<name>.monitor` | Output |
| `rule.<name>.width` | Column width in [cells](#column-widths), 1 to 16 |

```ini
rule.games.app-id = steam_app_.*
rule.games.workspace = 3
rule.chat.app-id = discord|org\.telegram\..*
rule.chat.workspace = chat
rule.chat.width = 1
rule.calc.app-id = org\.gnome\.Calculator
rule.calc.floating = on
```

- Every matching rule applies, in file order; later rules win.
- A rule never moves your focus or view.
- To find an app ID: `neferwl state | jq '.windows[].app_id'`.

## Output placement

By default, outputs sit left to right in config order. Place one against another, and use `offset` (logical pixels) to align the edges:

```ini
output.DP-2.right-of = DP-1
output.DP-2.offset = 360    # DP-2 starts 360 px lower than DP-1
```

```
┌────────────┐
│            │┌─────────┐
│    DP-1    ││  DP-2   │
│ 2560x1440  ││1920x1080│
│            │└─────────┘
└────────────┘
```

`offset` moves down for `right-of`/`left-of`, and right for `above`/`below`. kanshi, wdisplays and wlr-randr override these settings until the next reload.

## Workspace size

`workspace.<name>.size` gives a named workspace a smaller centered viewport, for example to present at 1080p on a 4K screen:

```ini
workspace.presentation.size = 1920x1080
bind.cmd+p = workspace presentation
```

## Touchpad

| Gesture | Action |
|---|---|
| Three fingers left/right | Scroll columns (switch workspace in cascade) |
| Three fingers up/down | Switch workspace (scroll bands in cascade) |
| Four fingers up | Open the overview, or hide the stash |
| Four fingers down | Close the overview, or show/hide the stash |

A swipe moves one step at most. Other gestures (pinch, hold, two-finger scroll) go to the app under the pointer.

## HDR

`output.<name>.hdr = on` sends HDR10 to displays that support it. If the display, driver or GPU cannot, NeferWL falls back to SDR and logs why. NVIDIA is not supported yet.

- HDR-aware apps (games, browsers, video players) can show HDR content. Some need HDR turned on in their own settings. X11 apps stay SDR.
- The desktop stays SDR, shown at `sdr-brightness` nits.
- Fullscreen HDR windows go straight to the display.
- Screenshots and captures are SDR (tone-mapped).
- If NeferWL crashes, the next compositor may show distorted colours until it sets its own colour pipeline.

## Binds

`bind.<keys> = <action>`. Modifiers: `cmd` (see `keyboard.cmd`), `shift`, `ctrl`, `alt`, `super`.

- Keys are what your layout prints: `cmd+é` on AZERTY. Write `=`, `#` and space by name: `cmd+equal`, `cmd+numbersign`, `cmd+space`.
- `code:N` is a physical key, the same on every layout: `code:2` to `code:10` are the digits 1 to 9.
- Other keys use their xkb name (`wev` shows it).
- `none` removes a default bind.
- `spawn <command>` runs a program without a shell. See [Running commands](desktop.md#running-commands).
- Holding a move bind repeats it: focus, move, resize and workspace steps, first after 250 ms, then 8 times a second. Toggles, spawns and closes run once. The overview's arrows and `h/j/k/l` repeat the same way.

```ini
bind.XF86AudioRaiseVolume = spawn wpctl set-volume -l 1.0 @DEFAULT_AUDIO_SINK@ 5%+
bind.XF86AudioLowerVolume = spawn wpctl set-volume @DEFAULT_AUDIO_SINK@ 5%-
bind.XF86AudioMute = spawn wpctl set-mute @DEFAULT_AUDIO_SINK@ toggle
bind.XF86MonBrightnessUp = spawn brightnessctl set +5%
bind.XF86MonBrightnessDown = spawn brightnessctl set 5%-
```

### Actions

| Action | Effect |
|---|---|
| `spawn <command>` | Run a program |
| `spawn-terminal` | Run `terminal` |
| `stash <command>` | Run a program and open its window in the [stash](#stash) |
| `close-window` | Close the focused window |
| `toggle-fullscreen` | Fullscreen the focused window. Nothing shows above it except its own dialogs |
| `toggle-floating` | Make the focused window [float](#free-floating-windows), or put it back in its column |
| `toggle-window-stash` | Move the focused window to the [stash](#stash), or back to its column |
| `toggle-stash-visible` | Show or hide the stash |
| `toggle-overview` | Open or close the [overview](#overview) |
| `switch-column-next` / `-prev` | Switch to the previously used column. Hold `cmd` and press again to go further back |
| `maximize-column` | Toggle full width for the focused column |
| `cycle-column-width` | Grow the focused column by one [cell](#column-widths), then back to equal widths |
| `set-column-width <+N\|-N>` | Widen or narrow the focused column by N cells |
| `set-window-height <+N%\|-N%>` | Change the focused window's share of its column |
| `focus-column-left/right` | Focus the neighbor column, then the neighbor monitor |
| `focus-window-up/down` | Focus the neighbor window in the column, then the neighbor workspace (band in cascade) |
| `move-column-left/right` | Move the focused column, then to the neighbor monitor |
| `move-window-up/down` | Move the focused window in its column |
| `consume-or-expel-window-left/right` | Join the neighbor column, or leave the current one |
| `focus-workspace <N>` / `-prev/-next` | Show a workspace |
| `workspace <name>` | Show or hide a named workspace |
| `move-column-to-workspace <N>` / `-prev/-next` | Move the focused column to a workspace |
| `move-window-to-workspace <N>` / `-prev/-next` | Move the focused window to a workspace |
| `move-workspace-prev/next` | Swap the workspace with its neighbor |
| `focus-monitor-left/right/up/down` | Focus the neighbor monitor |
| `move-workspace-to-monitor-left/right/up/down` | Move the workspace to the neighbor monitor |
| `scale-up` / `scale-down` | Zoom the display; the scale is saved to the config |
| `quit` | Exit NeferWL |

### Default binds

| Keys | Action |
|---|---|
| `cmd+return` | `spawn-terminal` |
| `ctrl+cmd+space` | `spawn fuzzel` |
| `cmd+q` | `close-window` |
| `cmd+left/right`, `cmd+h/l` | `focus-column-left/right` |
| `cmd+up/down`, `cmd+k/j` | `focus-window-up/down` |
| `cmd+shift+left/right`, `cmd+shift+h/l` | `move-column-left/right` |
| `cmd+shift+up/down`, `cmd+shift+k/j` | `move-window-up/down` |
| `cmd+bracketleft/bracketright` | `consume-or-expel-window-left/right` |
| `cmd+alt+left/right`, `cmd+alt+h/l` | `set-column-width -1/+1` |
| `cmd+alt+up/down`, `cmd+alt+k/j` | `set-window-height -10%/+10%` |
| `cmd+r` | `cycle-column-width` |
| `cmd+f` | `maximize-column` |
| `cmd+shift+f` | `toggle-fullscreen` |
| `cmd+shift+space` | `toggle-floating` |
| `cmd+s` | `toggle-stash-visible` |
| `cmd+shift+s` | `toggle-window-stash` |
| `cmd+o` | `toggle-overview` |
| `cmd+tab` / `cmd+shift+tab` | `switch-column-next/prev` |
| `cmd+1`…`9` (`code:2`…`10`) | `focus-workspace 1`…`9` |
| `cmd+shift+1`…`9` | `move-column-to-workspace 1`…`9` |
| `cmd+pageup/pagedown` | `focus-workspace-prev/next` |
| `cmd+shift+pageup/pagedown` | `move-column-to-workspace-prev/next` |
| `cmd+ctrl+arrows`, `cmd+ctrl+h/j/k/l` | `focus-monitor-*` |
| `cmd+ctrl+shift+left/right`, `cmd+ctrl+shift+h/l` | `move-workspace-to-monitor-left/right` |
| `cmd+ctrl+shift+up/down`, `cmd+ctrl+shift+k/j` | `move-workspace-prev/next` |
| `cmd+code:12` / `cmd+code:13` (`-` / `=` on US) | `scale-down` / `scale-up` |
| `ctrl+alt+backspace` | `quit` |

With `keyboard.cmd = alt` or `ctrl`, the defaults that would combine `cmd` with that same modifier are dropped. Bind those actions to other keys.

## Layout

### Cascade

The default layout. Columns fill the screen side by side up to `max-columns`, then continue in a new band below. Each band fills the screen height.

- Left/right focus moves within a band, then to the neighbor workspace.
- Up/down moves through the windows of a column, then to the band above or below.
- A maximized column gets a band of its own.

### Column widths

A cell is `1/max-columns` of the screen width. A column is 1 to 16 cells wide. Columns without a set width share the space equally.

### Stash

Each workspace has a stash: a strip of windows set aside with `toggle-window-stash`. The selected one shows centered over your columns, with its neighbors dimmed on the sides. `focus-column-left/right` or a three-finger swipe browse it. `toggle-stash-visible` or a four-finger swipe hides it.

`stash <command>` runs a program and opens its window straight in the stash of the workspace you were on, selected and focused, even if you switch workspace before it appears. With `stash.spawn-terminal`, `toggle-stash-visible` or the four-finger swipe down on an empty stash does the same with `terminal`.

```ini
bind.cmd+shift+f = stash firefox
```

The window must appear within 5 seconds, otherwise it opens normally. Apps that hand a launch to an already running instance (single-instance browsers, `footclient`) open their window normally.

### Free floating windows

`toggle-floating` turns the focused window into a floating window that stays where you put it. On a floating window, move binds move it by 50 pixels and resize binds resize it.

### Moving windows with the pointer

- `cmd` + left drag moves a window. Drop it on the top or bottom of another window to stack it there, or in the middle to swap.
- `cmd` + right drag resizes a floating window.
- `Escape` cancels the drag.

### Overview

`toggle-overview` (or a four-finger swipe up) shows previews of your workspaces. Arrows, `h/j/k/l` or three-finger swipes move the selection; `Return` or a click opens it; `Escape` goes back. `close-window` and the move-to-workspace binds work on the selected preview.

A workspace's stash shows as a pile of cards. Selecting the pile unfolds it, so you see every stashed window: a row under the workspace with `layout.overflow = cascade`, a column on its left otherwise. Moves along the unfolded pile pick a card; a pile longer than the screen scrolls with the selection. Leaving it folds it again.

With `layout.overflow = cascade`, workspaces sit side by side, each band of columns is a lane, and the stash sits under the lanes. Down (or fingers down on the touchpad, with natural scroll off) moves to the lane below; past the last lane it unfolds the stash. Left/right walk the cards, then go to the neighbor workspace past either end. Up leaves the stash for the last lane, at the column you came from.

Otherwise, left from the first column unfolds the stash. Up/down walk the cards and right goes back to the columns.
