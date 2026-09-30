# Configuration

NeferWL reads `$XDG_CONFIG_HOME/neferwl/config` (or `~/.config/neferwl/config`). `neferwl --config <path>` selects another file; `neferwl validate-config [path]` checks one without starting the compositor.

## Format

- One `key = value` per line; `#` starts a comment.
- Lists are comma-separated; booleans are `on` / `off`.
- A missing file or key means the default. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. A duplicate key warns and the last one wins.
- Every key applies live when the file is saved; windows stay open. `startup` and `xwayland` take effect at the next start.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `keyboard.layout`, `.variant`, `.options` | empty | xkb names, e.g. `fr`, `caps:escape` |
| `keyboard.repeat-rate` / `repeat-delay` | `25` / `600` | Key repeat per second / delay in ms |
| `keyboard.cmd` | `super` | Modifier that `cmd` means in binds: `super`, `alt` or `ctrl` |
| `terminal` | `$TERMINAL`, or `foot` if unset/empty | Command for automatic requests and `spawn-terminal`; whitespace-separated |
| `terminal.auto-open` | `first` | `first`: one request for the initial numbered workspace per session; `all`: visible empty workspaces without slots, with retry protection; `off`: none. `--no-terminal` disables automatic requests for this run only |
| `startup` | none | Command run once at session start; repeat the key for more |
| `xwayland` | `xwayland-satellite` | X11 support; `off` disables it |
| `background` | `#111111` | Solid background color |
| `border.width` | `2` | Lines between visible neighboring tiles and around non-fullscreen floats, in logical pixels; `0` hides them |
| `floating.dim` | `0.3` | Black veil over tiles and lower layers when a native floating window above the tiles or the stash is visible; opacity 0 to 1 |
| `stash.width` | `80` | Width of the selected stashed window, over the tiles, in percent of the usable width; 10 to 90 (`80%` works too). The rest is split between both sides, where the neighbors show. Its height is 80% of the usable height |
| `stash.gap` | `2` | Space between the selected stashed window and its neighbors, in percent of the usable width; 0 to 10. The neighbors show from there to the screen edge: with the defaults, 8% of each. A gap as wide as the side margin hides them |
| `stash.dim` | `0.5` | Black veil over the stash neighbors, borders included; opacity 0 to 1 |
| `border.active` / `border.inactive` | `#808080` / `#111111` | Colors for focused / other window lines; only the focused output lights up |
| `layout.gaps` | `0` | Space between windows |
| `layout.max-columns` | `2` | Columns that share the screen before scrolling |
| `layout.overflow` | `scroll` | Past the max: `scroll` right, or `fixed` spiral split |
| `layout.<output>.max-columns`, `.overflow` | layout values | Per-screen layout: connector (`DP-2`) or monitor key |
| `layout.presets` | `1/3, 1/2, 2/3, 1` | Widths for `cycle-column-width` (scroll overflow) |
| `touchpad.natural-scroll` | `off` | Content follows the fingers, for two-finger scroll and three-finger swipes (see [Touchpad](#touchpad)) |
| `touchpad.tap` | `on` | Tap to click: one finger left, two right, three middle |
| `touchpad.accel-speed` | `0` | Touchpad pointer speed, from `-1` (slowest) to `1` (fastest) |
| `touchpad.accel-profile` | `adaptive` | `adaptive`: faster finger moves go further; `flat`: constant speed |
| `touchpad.scroll-factor` | `1` | Multiplies two-finger scroll distance, above `0` up to `10` |
| `focus.follow-move` | `off` | Follow a column moved to another workspace |
| `workspace.<name>.*` | none | Named workspaces are outside the numbered list; each needs a `workspace <name>` bind to show it and toggle back |
| `workspace.<name>.monitor` | first output | Home monitor: connector (`DP-2`) or monitor key; guests on another output while home is absent and returns when it reconnects |
| `workspace.<name>.max-columns`, `.overflow` | screen values | Per-workspace layout |
| `workspace.<name>.column.<N>` | none | Slot: `<width>, <command>` |
| `output.<name>` | preferred | `WxH`, `WxH@Hz`, `preferred` or `off` |
| `output.<name>.scale` | `1` | 1 to 4, e.g. `1.5` or `4/3` |
| `output.<name>.primary` | `off` | Gets focus and pointer at startup |
| `output.<name>.hdr` | `off` | Turns on HDR10 on capable outputs; otherwise falls back to SDR (reason in the log) |
| `output.<name>.sdr-brightness` | `203` | How bright SDR desktop content appears in HDR, in nits (80–1000) |
| `render.direct-scanout` | `on` | Fullscreen buffers straight to the display |
| `render.tearing` / `render.vrr` | `on` / `on` | Tearing: games in direct scanout. VRR: any fullscreen window covering the output |
| `render.vrr-flip-gap` | `1ms` | Under VRR, a game frame commits at least this long after the previous flip, so it does not wait for the panel's slowest refresh (0 to 10ms; `0` turns it off) |
| `performance.realtime` | `on` | Request real-time scheduling for output and input threads |
| `log.level` / `log.debug` | `info` / empty | Log level / debug components or `all` |
| `bind.<keys>` | see below | Action for a key combo; `none` removes a default |

## Touchpad

A three-finger swipe follows the fingers:

- Left or right scrolls the columns (`scroll` overflow). When the fingers lift, the view keeps the swipe's speed, slows down and settles on a column edge; the focus moves to a column fully on screen.
- Up or down slides between numbered workspaces and settles on the nearest one the swipe's speed reaches. It stops with some resistance at the first and last workspace.

With `natural-scroll = off`, a swipe left shows the columns to the left and a swipe up shows the workspace above. `natural-scroll = on` moves the content with the fingers, so both are reversed.

Where the view cannot scroll (`fixed` overflow, the stash, a floating or fullscreen window, a named workspace), a quick swipe runs `focus-column-left/right` or `focus-workspace-up/down` when the fingers lift.

Two-finger scroll goes to the window under the pointer with the touchpad's timestamps, so apps with kinetic scrolling keep their inertia.

## HDR

`output.<name>.hdr = on` sends HDR10 (BT.2020, PQ) to displays whose EDID and connector support it. The desktop stays SDR content, shown at `sdr-brightness` nits.

HDR-aware Wayland clients, including games through Vulkan WSI, browsers, and video players, can present HDR through `wp_color_manager_v1` and `wp_color_representation_v1`. Some apps require HDR to be enabled in their own settings. X11 clients stay SDR.

Hardware-decoded video can use NV12 (8-bit) or P010 (10-bit) DMA-BUF subsurfaces with BT.601/709/2020 coefficients, full or limited range, and chroma-location type 0. The Vulkan device must support sampling the format/modifier with per-plane views; planes in one DMA-BUF use one memory import, while separate plane buffers additionally require Vulkan DISJOINT support. Unsupported formats are not offered. Confirm hardware decoding and the advertised DMA-BUF formats when testing an app.

- Fullscreen HDR buffers (10-bit, PQ) go straight to the display unchanged.
- Windowed and otherwise composed HDR content keeps its full range in a linear fp16 image before PQ output; the display tone-maps it according to its EDID metadata. SDR surfaces are blended in linear light on HDR outputs, so antialiased edges may look slightly different than sRGB-space blending. Dim veils (`floating.dim`, `stash.dim`) darken SDR white as much as on an SDR output; brighter HDR highlights stay a little brighter.
- Screenshots and capture use opaque 8-bit sRGB. HDR capture applies GPU tone mapping and reduces out-of-sRGB gamut: values below 90% of SDR reference white stay unchanged, while brighter values are compressed to preserve highlight differences (SDR reference white maps to about 245/255 when capturing an HDR output, versus 255/255 on an SDR output). This capture-only transform does not change HDR display output.
- The display gets the metadata from its own EDID, not the client's MaxCLL/MaxFALL.

HDR requires DRM HDR connector properties, suitable KMS planes, and Vulkan fp16 composition and 10-bit export formats/features. Unsupported capabilities fall back to SDR. NVIDIA is not supported yet (tested status).

## Binds

`bind.<keys> = <action>`. Modifiers: `cmd` (see `keyboard.cmd`), `shift`, `ctrl`, `alt`, `super`.

- Keys are what the active layout prints: `cmd+é` on AZERTY. Write `=`, `#` and space by name: `cmd+equal`, `cmd+numbersign`, `cmd+space`.
- `code:N` is a physical key (evdev code), the same on every layout: `code:2` to `code:10` are the digit row 1 to 9.
- Other keys use their xkb keysym name, case-sensitive (`wev` shows it). Media keys have no default bind:

  ```
  bind.XF86AudioRaiseVolume = spawn wpctl set-volume -l 1.0 @DEFAULT_AUDIO_SINK@ 5%+
  bind.XF86AudioLowerVolume = spawn wpctl set-volume @DEFAULT_AUDIO_SINK@ 5%-
  bind.XF86AudioMute = spawn wpctl set-mute @DEFAULT_AUDIO_SINK@ toggle
  bind.XF86MonBrightnessUp = spawn brightnessctl set +5%
  bind.XF86MonBrightnessDown = spawn brightnessctl set 5%-
  ```
- `none` removes a default bind.
- `spawn <command>` runs a program without a shell; see [Running commands](desktop.md#running-commands).

### Actions

| Action | Effect |
|---|---|
| `spawn <command>` | Run a program |
| `spawn-terminal` | Run `terminal` |
| `close-window` | Close the focused window |
| `toggle-fullscreen` | Fullscreen the focused window. An app that asks for fullscreen in its first second (e.g. a Wine launcher that remembered a monitor-sized window) opens in a column instead; later requests are honoured. Fullscreen is exclusive, whether from this bind or the app: nothing is drawn above it but a surface taking the keyboard (a locker). Bars, notifications and other windows, dialogs included, wait hidden until it leaves; activating one of them leaves fullscreen. In fixed overflow, the window gets its own workspace below its home one; a window opening while it is on screen tiles on the home workspace and the view stays on the fullscreen window; activating it shows the home workspace |
| `toggle-window-stash` | Move the focused tile to the end of the workspace's [stash](#stash), shown and selected; on a stashed window, send it back to its former column, width and maximized or expanded state when possible. A native dialog becomes a new tiled column. A fullscreen window must leave fullscreen first |
| `toggle-stash-visible` | Hide the stash and give the focus back to the tiles, or show it again with the focus on its selected window. Native dialogs stay as they are. Does nothing under a fullscreen window |
| `toggle-overview` | Open the [overview](#overview), or close it on the selected window |
| `maximize-column` | Toggle full usable width for the focused column, preserving gaps and its saved width; in fixed overflow, other columns are hidden until focus moves or it is toggled off. On a window that made itself fullscreen (e.g. a Wine app at monitor size), it first returns the window to its column |
| `cycle-column-width` | Step through `layout.presets`. In fixed overflow, toggle the focused column to `max-columns - 1` cells in place; the other columns stack on each side in the last cell. One column is expanded at a time |
| `focus-column-left/right` | Focus the neighbor column; at the edge, the neighbor monitor. In fixed overflow, the neighbor on screen |
| `focus-window-up/down` | Focus in the column; past the edge, the next workspace. In fixed overflow, the column on screen above or below comes first. Up at the top brings back a covering float put behind the columns |
| `move-column-left/right` | Move the focused column |
| `consume-or-expel-window-left/right` | A lone window joins the neighbor column; a stacked one leaves for a new column |
| `focus-workspace <N>` / `focus-workspace-up/down` | Show a numbered or neighbor workspace |
| `workspace <name>` | Toggle a named workspace |
| `move-column-to-workspace <N>` / `-up/-down` | Move the focused column |
| `move-window-to-workspace <N>` / `-up/-down` | Move only the focused window |
| `focus-monitor-left/right` | Focus the neighbor monitor |
| `move-workspace-to-monitor-left/right` | Move the workspace; it gets a new home |
| `scale-up` / `scale-down` | Zoom the whole display; one second after the last press, the scale is saved to `output.<name>.scale` and a notification confirms it (`notify-send`) |
| `quit` | Exit NeferWL |

### Stash

Each workspace has a stash: a horizontal strip of windows set aside with `toggle-window-stash`, in the order they arrived. The selected window is centred over the tiles, `stash.width` of the usable width (80% by default) and 80% of its height. Its left and right neighbors sit `stash.gap` beside it and show up to the screen edges, dimmed (`stash.dim`); a click on one selects it. The others wait off screen.

While the stash has the focus, `focus-column-left/right` and the three-finger swipe move through it and stop at its ends; `focus-window-up/down` do nothing. Hide it with `toggle-stash-visible` to get back to the tiles. Scripts see each stashed window's place, and whether the stash is hidden, in the [state file](desktop.md#state-for-scripts).

Native floating windows are not in the stash: they stay centred in the usable output area. A window-sized float that fills that area (within two border widths plus two logical pixels per axis) stays below the columns when you focus a tile; `focus-window-up` at the top of a column raises it again. It does not hide bars or pin focus. Small dialogs and file pickers always stay above the columns; shrinking a float promotes it above them. Real fullscreen remains exclusive.

### Overview

`toggle-overview` shows scaled previews of the current workspace, including over fullscreen windows. Windows keep their size and previews show their last frames. Covering floats and windows hidden behind a maximized column in `fixed` overflow appear as cards: the on-screen item is in front, hidden columns share one spiral-layout card, up to two cards behind peek above it, and passed cards peek below. Neighbor workspaces show dimmed stacks. Smaller floats, such as dialogs, stay hidden. A fullscreen floating window, or any fullscreen window with `fixed` overflow, is its workspace's only preview; `scroll` overflow keeps its columns selectable in one card.

| Key | Action |
| --- | --- |
| `h` / `l`, `left` / `right` | Move within a front column-group card; left of its first tile enters the stash. Left on a single-window card sends it behind, or enters the stash when none is behind; right does nothing on a single card. |
| `k` / `j`, `up` / `down` | Bring the next card above / previous card below to the front, then change workspace at the stack boundary |
| `return` | Show the selected front card; picking a hidden column moves the maximization to it |
| `escape` | Restore the original focus, maximization and float order |

The focus binds (`focus-column-left/right`, `focus-window-up/down`, `cmd+arrows` by default) move the selection like these keys.

Each workspace's [stash](#stash), hidden or not, shows as a pile of cards on the left of its row, which stays centred unless it would overlap the pile: its selected window in front, up to three others behind it, dimmed. In the pile, `h` / `l` browse the stash and `l` past its last window returns to the front card. A successful up/down move through the stack leaves the stash and selects the new front card. `return` or a click on a card closes the overview with the stash shown on that window.

A four-finger swipe up opens the overview and a swipe down closes it on the selection, whatever `touchpad.natural-scroll` says. Two-finger scrolling and the mouse wheel move the selection: left and right through the columns, up and down through stack cards before crossing workspaces, following `touchpad.natural-scroll`. A three-finger swipe moves it one step when the fingers lift.

Return or a click on a front tile or peeking card commits it; card changes remain provisional until then. `close-window` targets the selected preview. Window mutation binds (moving, resizing, maximizing, fullscreen and stash toggles) and workspace moves are disabled while the overview is open. Workspace and monitor navigation, launch and quit binds remain active.

### Default binds

| Keys | Action |
|---|---|
| `cmd+return` | `spawn-terminal` |
| `cmd+left` | `focus-column-left` |
| `cmd+right` | `focus-column-right` |
| `cmd+up` | `focus-window-up` |
| `cmd+down` | `focus-window-down` |
| `cmd+shift+left` | `move-column-left` |
| `cmd+shift+right` | `move-column-right` |
| `cmd+h` | `focus-column-left` |
| `cmd+l` | `focus-column-right` |
| `cmd+k` | `focus-window-up` |
| `cmd+j` | `focus-window-down` |
| `cmd+shift+h` | `move-column-left` |
| `cmd+shift+l` | `move-column-right` |
| `cmd+bracketleft` | `consume-or-expel-window-left` |
| `cmd+bracketright` | `consume-or-expel-window-right` |
| `cmd+r` | `cycle-column-width` |
| `cmd+f` | `maximize-column` |
| `cmd+shift+f` | `toggle-fullscreen` |
| `cmd+s` | `toggle-stash-visible` |
| `cmd+shift+s` | `toggle-window-stash` |
| `cmd+o` | `toggle-overview` |
| `cmd+q` | `close-window` |
| `ctrl+alt+backspace` | `quit` |
| `ctrl+cmd+space` | `spawn fuzzel` |
| `cmd+pageup` | `focus-workspace-up` |
| `cmd+pagedown` | `focus-workspace-down` |
| `cmd+shift+pageup` | `move-column-to-workspace-up` |
| `cmd+shift+pagedown` | `move-column-to-workspace-down` |
| `cmd+code:2` | `focus-workspace 1` |
| `cmd+code:3` | `focus-workspace 2` |
| `cmd+code:4` | `focus-workspace 3` |
| `cmd+code:5` | `focus-workspace 4` |
| `cmd+code:6` | `focus-workspace 5` |
| `cmd+code:7` | `focus-workspace 6` |
| `cmd+code:8` | `focus-workspace 7` |
| `cmd+code:9` | `focus-workspace 8` |
| `cmd+code:10` | `focus-workspace 9` |
| `cmd+shift+code:2` | `move-column-to-workspace 1` |
| `cmd+shift+code:3` | `move-column-to-workspace 2` |
| `cmd+shift+code:4` | `move-column-to-workspace 3` |
| `cmd+shift+code:5` | `move-column-to-workspace 4` |
| `cmd+shift+code:6` | `move-column-to-workspace 5` |
| `cmd+shift+code:7` | `move-column-to-workspace 6` |
| `cmd+shift+code:8` | `move-column-to-workspace 7` |
| `cmd+shift+code:9` | `move-column-to-workspace 8` |
| `cmd+shift+code:10` | `move-column-to-workspace 9` |
| `cmd+ctrl+left` | `focus-monitor-left` |
| `cmd+ctrl+right` | `focus-monitor-right` |
| `cmd+ctrl+h` | `focus-monitor-left` |
| `cmd+ctrl+l` | `focus-monitor-right` |
| `cmd+ctrl+shift+left` | `move-workspace-to-monitor-left` |
| `cmd+ctrl+shift+right` | `move-workspace-to-monitor-right` |
| `cmd+ctrl+shift+h` | `move-workspace-to-monitor-left` |
| `cmd+ctrl+shift+l` | `move-workspace-to-monitor-right` |
| `cmd+code:13` | `scale-up` |
| `cmd+code:12` | `scale-down` |
