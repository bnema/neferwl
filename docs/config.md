# Configuration

NeferWL reads `~/.config/neferwl/config` (or `$XDG_CONFIG_HOME/neferwl/config`). Changes apply when you save, except `startup` and `xwayland`, which apply at the next start.

```sh
neferwl --config <path>            # use another file
neferwl validate-config [path]     # check a file
```

Missing keys use defaults. Empty keyboard values use the system setting. No startup commands or named workspaces are configured by default. The log lists changed keys after a reload.

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
| `border.radius` | `0` | Corner radius in logical pixels, `0` to `64`; floats always, tiles only with gaps |
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
| `focus.position-hint` | `on` | Show workspace/band dots near the output bottom for 600ms after a position change, then fade for 150ms (no fade with animations off); hidden in overview, fullscreen and session lock; never captured |
| `focus.flash` | `on` | Briefly draw an inner border, inside the window border, on the window, column or output an action changed |
| `focus.flash-width` | `2` | Flash border width in logical pixels, `1` to `8` |
| `focus.flash-opacity` | `0.6` | Starting opacity of the flash, `0.1` to `1` |
| `focus.flash-duration` | `200ms` | Fade duration, `50ms` to `1s`; not scaled by `animations.speed` |
| `focus.flash-color` | `border.active` | Flash color `#rrggbb`; empty uses `border.active` |
| `hint.enable` | `off` | Turn on the experimental [hint mode](#hint-mode) (restart required) |
| `hint.targets` | `clickable` | What [hint mode](#hint-mode) labels: `clickable` (links, URLs and buttons) or `links` (links and URLs only) |
| `hint.text-click` | `ctrl+shift` | Modifiers held for clicks on links found by OCR (terminals open links on a modified click); `none` sends a plain click |
| `hint.capture` | `off` | Show hint labels in captures requested by Wayland clients (screenshots, screencasts); the compositor's own hint scans never see them; applies on reload |
| `hint.size` | `auto` | Label text size in logical pixels (6 to 72), or `auto` for screen pixel density |
| `hint.background` | empty | Label background (`#rrggbb`); empty uses `border.active` |
| `hint.foreground` | empty | Label text (`#rrggbb`); empty chooses a contrasting colour |
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

With `layout.gaps = 0`, borders are tmux-style lines between tiles. With gaps, every tile gets a full border, even alone. `border.radius` rounds floating windows always and tiles with gaps; fullscreen windows, popups and layers stay square.

`<output>` is a connector such as `DP-2`, or a monitor key (`make model serial`).

The position hint follows actual workspace/band changes and monitor focus, regardless
of input method. It lists all ext-workspace entries in monitor order: numbered
workspaces (including the empty spare), then configured hidden named workspaces,
even when empty. Cascade workspaces use top-aligned band columns; scroll/fixed
workspaces use one vertical workspace column. Large lists show at most 16 entries
with the active entry kept visible; band rows are capped at 16.

The focus flash marks the window, column or output an action changes with a brief
inner border. When a flash-triggering action crosses screens, it marks the whole
destination output, even if that output has no windows. Same-screen workspace
switches and mouse focus do not trigger a flash.
It runs when `animations = on`.

## Automatic terminal

The command comes from `terminal`, then `$TERMINAL`, then `foot`.

| `terminal.auto-open` | Behaviour |
| --- | --- |
| `first` | Open once on the initial numbered workspace |
| `all` | Open on each visible empty workspace without predefined slots |
| `off` | Do not open automatically |

`--no-terminal` disables automatic opening for one run; `spawn-terminal` still works. Launched programs inherit `$SHELL`, `$EDITOR` and `$VISUAL`.

## Per-output defaults

Per-output defaults (replace `<name>` with a connector such as `DP-1`):

```text
output.<name> = preferred
output.<name>.scale = 1
output.<name>.transform = normal
output.<name>.primary = off
output.<name>.offset = 0
output.<name>.hdr = off
output.<name>.sdr-brightness = 203
```

Outputs are arranged automatically unless `.right-of`, `.left-of`, `.above` or `.below` is set. Per-output layouts inherit `layout.*`; named workspaces inherit their output's layout and size, use the focused output unless `.monitor` is set, and have no predefined columns. Media keys have no default bindings.

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
| `hint` | Label the links and buttons on the focused screen, then click the one you type ([hint mode](#hint-mode)) |
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

The default layout. Columns sit side by side in bands of up to `max-columns`, stacked one below the other. Each band fills the screen height and keeps its columns until you move them.

- Left/right focus moves within a band, then to the neighbor monitor, then to the neighbor workspace.
- `move-column-left/right` stays in the band; at its edge the column goes to the neighbor monitor, if any.
- Up/down moves through the windows of a column, then to the band above or below.
- `move-window-up/down` past the column's top or bottom sends the whole column to the band above or below. It joins that band when it has room, else it gets a new band between the two.
- A new window joins the focused band when it has room, else it gets a new band right below.
- `cycle-column-width` in a full band makes room: the rightmost column goes to the band below (from the last column, the leftmost goes to the band above). The end of the cycle brings it back.
- `maximize-column` gives the column a band of its own: the columns on its left go to the band above, the ones on its right to the band below. Maximizing again forms the band back.
- A fullscreen window gets a band of its own right below the band it was in (expelled into its own column first when it shares one), so the other columns stay put and focus changes among them leave the fullscreen output alone. Leaving fullscreen keeps the column in that band.
- `move-column-left/right` on a fullscreen window (tiled or floating) sends it to the neighbor monitor, where it stays fullscreen and focused; with no neighbor monitor nothing moves.
- A band left empty goes away. A smaller `max-columns` moves the extra columns of a band to a new band below it.

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
- `cmd` + right drag on the line between two columns or two stacked windows resizes them.
- `cmd` + double right-click on that line resets the lane's column widths or the column's row heights. Column widths reset when a column enters or leaves the lane.
- `Escape` cancels the drag and restores the previous sizes.

### Overview

`toggle-overview` (or a four-finger swipe up) shows previews of your workspaces. Arrows, `h/j/k/l` or three-finger swipes move the selection; `Return` or a click opens it; `Escape` goes back. `close-window` and the move-to-workspace binds work on the selected preview.

A workspace's stash shows as a pile of cards. Selecting the pile unfolds it into a carousel of every stashed window: a row under the workspace with `layout.overflow = cascade`, a column on its left otherwise. The selected card stays in the middle, the earlier cards before it and the later ones after it, wrapping round; a wider gap separates the newest card from the oldest. Moves along the carousel pick a card. With fewer than three cards, or on a workspace holding only stashed windows, it does not wrap round. Leaving it folds it again. Moving to another workspace selects its windows rather than a carousel that wraps round, even when its stash has the focus, so browsing workspaces never stops on one.

With `layout.overflow = cascade`, workspaces sit side by side, each band of columns is a lane, and the stash sits under the lanes. Down (or fingers down on the touchpad, with natural scroll off) moves to the lane below; past the last lane it unfolds the stash. Left/right walk the cards; where the carousel does not wrap round, they go to the neighbor workspace past either end. Up leaves the stash for the last lane, at the column you came from.

Otherwise, left from the first column unfolds the stash. Up/down walk the cards, the focus-workspace binds change workspace, and right goes back to the columns.

## Hint mode

Hint mode is experimental and off by default: set `hint.enable = on` and restart NeferWL.
While it is off, nothing of hint mode runs or uses memory, and the `hint` bind
logs a notice once and does nothing. A reload does not change it.

`hint` labels the links and buttons on the focused output. Type a label to
click its target; BackSpace erases a letter, Escape cancels. No bind is set
by default; for example `bind.cmd+g = hint`.

Labels use the `border.active` colour with a contrasting text colour, and
their size follows the screen's pixel density. Each label sits just outside
the top-left corner of its target. Links found by OCR (terminal text) are
clicked with the `hint.text-click` modifiers held, which is how terminals open
a link; accessibility targets get a plain click.

Labels are not part of screenshots and screencasts by default. With
`hint.capture = on` they appear in the captures that Wayland clients request
(including Exclude captures); NeferWL's own text recognition never sees them.
The capture indicator and the position hint stay out of every capture.
A reload applies `hint.capture` to the next frame.

### Where targets come from

| Source | Apps | Accuracy | Speed |
|---|---|---|---|
| Accessibility tree (AT-SPI) | Firefox, Chromium, GTK, Qt, Electron | Exact: the app reports its own links and buttons | A few milliseconds |
| Text recognition (OCR) | Everything else: terminals, apps without accessibility | Best effort: URLs, domains and underlined text written on screen | 150 ms or more, more on large screens |

NeferWL asks the accessibility tree first. Windows it does not cover are read
with OCR, which finds visible URLs, domains, email addresses and underlined
text. Underlined words get a plain click, and the words of one continuous
underline form one target; URLs keep the `hint.text-click` modifiers. OCR
cannot see a link whose address is hidden behind plain text, and it can misread
small or low-contrast text.

### Requirements

- Accessibility: NeferWL enables it lazily on the first hint scan that queries
  app windows. Apps started before then may need restarting. Some apps need a flag:
  - Chromium: `--force-renderer-accessibility`
  - Electron: `ACCESSIBILITY_ENABLED=1`
  - Qt: `QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1`
- OCR: `tesseract` and `tesseract-data-eng`. Without tesseract, OCR is off
  and hint mode labels only apps that expose the accessibility tree.

### Debugging hint mode

With `hint.enable = on` and `NEFERWL_HINT_DEBUG=1` set when NeferWL starts (for example `NEFERWL_HINT_DEBUG=1 make tty`, or the variable in the launch environment), every successful scan writes three files to `$XDG_STATE_HOME/neferwl/hints/` (`~/.local/state/neferwl/hints/` when unset): `<time>-<output>.png`, the frame as scanned; `<time>-<output>.annotated.png`, the same with each target outlined (OCR links green, accessibility targets magenta, OCR window crops grey); and `<time>-<output>.json`, the targets (with their `source`, `atspi` or `ocr`) and timings. NeferWL keeps the 50 most recent scans and logs a warning at startup. The files contain the screen in clear: delete them after use.
