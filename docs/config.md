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
| `startup` | none | Commands run once at session start; separate several with `;` or repeat the key |
| `xwayland` | `xwayland-satellite` | X11 support; `off` disables it |
| `background` | `#111111` | Solid background color |
| `border.width` | `2` | Lines between visible neighboring tiles and around non-fullscreen floats, in logical pixels; `0` hides them |
| `floating.dim` | `0.3` | Black veil over tiles and lower layers when a native floating window above the tiles or the stash is visible, and over the background and lower layers behind the overview; opacity 0 to 1 |
| `stash.width` | `80` | Width of the selected stashed window, over the tiles, in percent of the usable width; 10 to 90 (`80%` works too). The rest is split between both sides, where the neighbors show. Its height is 80% of the usable height |
| `stash.gap` | `2` | Space between the selected stashed window and its neighbors, in percent of the usable width; 0 to 10. The neighbors show from there to the screen edge: with the defaults, 8% of each. A gap as wide as the side margin hides them |
| `stash.dim` | `0.5` | Black veil over the stash neighbors, borders included; opacity 0 to 1 |
| `stash.capture` | `on` | `on`: a window opened while the stash is shown joins its end, selected; `toggle-window-stash` tiles it as a new column. `off`: new windows always tile |
| `border.active` / `border.inactive` | `#808080` / `#111111` | Colors for focused / other window lines; only the focused output lights up |
| `layout.gaps` | `0` | Space between windows |
| `layout.max-columns` | `2` | Columns that share the screen before scrolling |
| `layout.overflow` | `scroll` | Past the max: `scroll` right, `fixed` spiral split, or `cascade` into vertically scrolling bands |
| `layout.<output>.max-columns`, `.overflow` | layout values | Per-screen layout: connector (`DP-2`) or monitor key |
| `layout.presets` | `1/3, 1/2, 2/3, 1` | Widths for `cycle-column-width` (scroll overflow) |
| `touchpad.natural-scroll` | `off` | Content follows the fingers, for two-finger scroll and three-finger swipes (see [Touchpad](#touchpad)) |
| `touchpad.tap` | `on` | Tap to click: one finger left, two right, three middle |
| `touchpad.accel-speed` | `0` | Touchpad pointer speed, from `-1` (slowest) to `1` (fastest) |
| `touchpad.accel-profile` | `adaptive` | `adaptive`: faster finger moves go further; `flat`: constant speed |
| `touchpad.left-handed` | `off` | Swaps the left and right touchpad buttons |
| `touchpad.scroll-factor` | `1` | Multiplies two-finger scroll distance, above `0` up to `10` |
| `mouse.natural-scroll` | `off` | Wheel scrolling moves the content with the wheel instead of against it. `mouse.*` applies to every pointer that is not a touchpad: mice, trackballs, trackpoints |
| `mouse.accel-speed` | `0` | Mouse pointer speed, from `-1` (slowest) to `1` (fastest) |
| `mouse.accel-profile` | `adaptive` | `adaptive`: faster moves go further; `flat`: constant speed |
| `mouse.left-handed` | `off` | Swaps the left and right mouse buttons |
| `cursor.hide-after` | `5s` | Hides the pointer cursor after this long without motion; the next motion shows it. 100ms to 1h, or `off` |
| `animations` | `off` | **Experimental.** `on` animates changes; `off` makes every change instant and the three-finger swipe no longer follows the fingers: swipe landings, scrolling, workspace switches, window moves and resizes, drag drops, workspaces sent to another monitor, the overview, windows opening and closing, the stash showing and hiding, the focus pulse |
| `animations.speed` | `normal` | Speed of every spring (not the focus pulse): `slow`, `normal` or `fast` (a view move lands in about 260, 180 or 130 ms), or a number relative to `normal`, from `0.1` (10× slower) to `10` (10× faster) |
| `focus.follow-move` | `off` | Follow a column moved to another workspace |
| `focus.animation` | `pulse` | Focus indicator on a window that keeps the focus for 150 ms: `pulse` (brief rise and fall) or `off`. Skipped for fullscreen windows, a window alone on screen (e.g. a maximized column), the overview and a window focused as it opens. Off while `animations = off` |
| `focus.effect` | `screen` | What the focus indicator draws: `screen` brightens the window slightly, visible on black too. HDR (PQ) client content is not brightened on HDR outputs |
| `focus.strength` | `0.04` | Peak of the focus effect, `0.01` to `0.2`: black reaches about 10/255 at `0.04` |
| `workspace.<name>.*` | none | Named workspaces are outside the numbered list; use the overview to show an occupied one, or a `workspace <name>` bind to show it and toggle back |
| `workspace.<name>.monitor` | focused output | Home monitor: connector (`DP-2`) or monitor key. A `workspace <name>` bind focuses it there. Unset, or while home is absent, the workspace comes to the focused output; it returns home when it reconnects |
| `workspace.<name>.size` | `inherit` | Logical `WxH` viewport, centered on the monitor; inherits monitor size by default and always keeps its scale |
| `workspace.<name>.max-columns`, `.overflow` | screen values | Per-workspace layout |
| `workspace.<name>.column.<N>` | none | Slot: `<width>, <command>` |
| `output.<name>` | preferred | `WxH`, `WxH@Hz`, `preferred` or `off` |
| `output.<name>.scale` | `1` | 1 to 4, e.g. `1.5` or `4/3` |
| `output.<name>.transform` | `normal` | `normal`, `90`, `180`, `270`, `flipped`, `flipped-90`, `flipped-180`, `flipped-270`; 90/270 swap the output's width and height |
| `output.<name>.primary` | `off` | Gets focus and pointer at startup |
| `output.<name>.right-of` / `.left-of` / `.above` / `.below` | automatic | Connector to place the output against, e.g. `DP-1` |
| `output.<name>.offset` | `0` | Logical px along the shared edge, −65536 to 65536; see [Output placement](#output-placement) |
| `output.<name>.hdr` | `off` | Turns on HDR10 on capable outputs; otherwise falls back to SDR (reason in the log) |
| `output.<name>.sdr-brightness` | `203` | How bright SDR desktop content appears in HDR, in nits (80–1000) |
| `render.direct-scanout` | `on` | Fullscreen buffers straight to the display |
| `render.tearing` / `render.vrr` | `on` / `on` | Tearing: games in direct scanout. VRR: any fullscreen window covering the output |
| `render.vrr-flip-gap` | `1ms` | Under VRR, a game frame commits at least this long after the previous flip, so it does not wait for the panel's slowest refresh (0 to 10ms; `0` turns it off) |
| `performance.realtime` | `on` | Request real-time scheduling for output and input threads |
| `log.level` / `log.debug` | `info` / empty | Log level / debug components or `all` |
| `bind.<keys>` | see below | Action for a key combo; `none` removes a default |

## Output placement

Without relations, outputs sit left to right in config order, top-aligned. A relation places an output against another one; `offset` slides it along the shared edge so the pointer crosses where the screens really meet.

```ini
output.DP-2.right-of = DP-1
output.DP-2.offset = 360    # DP-2 starts 360 px lower than DP-1's top
```

```
┌────────────┐
│            │┌─────────┐
│    DP-1    ││  DP-2   │
│ 2560x1440  ││1920x1080│
│            │└─────────┘
└────────────┘
```

Placement, offsets and monitor navigation use the rotated size: a 2560x1440 output with `transform = 90` is 1440 wide and 2560 tall at scale 1. Rotated outputs always compose, with no direct scanout or overlay planes.

Rules:

- `offset` moves down for `right-of`/`left-of` and right for `above`/`below`; negative moves the other way.
- Sizes are logical: the layout follows scale changes on its own.
- An output whose reference is not connected is placed automatically until the reference returns.
- Cycles and invalid values are ignored with a warning in the log.
- Positions and transforms set by kanshi, wdisplays or wlr-randr win until the next config reload.
- `focus-monitor-*`, `move-workspace-to-monitor-*` and column moves past the edge go to the output on that side.

Two rows of three, with DP-1 as the base:

```
┌────────┐┌────────────┐┌────────┐
│  DP-4  ││   HDMI-1   ││  DP-5  │
└────────┘└────────────┘└────────┘
┌────────┐┌────────────┐┌────────┐
│  DP-2  ││    DP-1    ││  DP-3  │
└────────┘└────────────┘└────────┘
```

```ini
# Bottom row: 1080p sides, bottoms aligned with DP-1 (1440 - 1080 = 360)
output.DP-2.left-of = DP-1
output.DP-2.offset = 360
output.DP-3.right-of = DP-1
output.DP-3.offset = 360

# Top row: 1200p sides, bottoms aligned with HDMI-1 (1440 - 1200 = 240)
output.HDMI-1.above = DP-1
output.DP-4.left-of = HDMI-1
output.DP-4.offset = 240
output.DP-5.right-of = HDMI-1
output.DP-5.offset = 240
```

## Workspace size

A workspace inherits its monitor size unless `workspace.<name>.size` sets a logical `WxH`. Its viewport is centered, and columns, maximization, floating windows and fullscreen use that real layout size—not scaled overview previews. Oversized dimensions are limited to the monitor; reserved layer zones still reduce usable space. Monitor mode and scale stay unchanged. Omit `size` or use `inherit` to restore the default. Workspace transitions involving a smaller viewport switch without the vertical slide, keeping scrolling columns inside their frame. Fullscreen inside a smaller viewport uses composition rather than output-covering direct scanout, so fullscreen-game VRR/tearing eligibility does not apply.

```ini
workspace.presentation.size = 1920x1080
bind.Cmd+p = workspace presentation
```

## Touchpad

With `animations = on`, a three-finger swipe follows the fingers and moves one step at most, so a quick swipe never skips a column or a workspace:

- Left or right scrolls the columns (`scroll` overflow) to the next column edge; the focus moves to a column fully on screen.
- Up or down slides to the next numbered workspace in scroll and fixed layouts.
- In [Cascade](#cascade), up/down scrolls one band and left/right switches workspaces.

The view sticks a little near each step and catches up between them. Past the next step it resists; lift and swipe again to go further. When the fingers lift, a quick swipe goes on to the next step and a slow one settles on the closest.

In scroll and fixed layouts with `natural-scroll = off`, a swipe left shows the columns to the left and a swipe up shows the workspace above. `natural-scroll = on` moves the content with the fingers, so both are reversed.

With `animations = off`, nothing moves during the swipe: when the fingers lift, a quick or long enough swipe navigates content or workspaces along the layout's axes. Cascade band swipes stop at the first and last band.

Over the shown stash a sideways swipe slides it with the fingers, with a stop on each window, and moves one window at most. In scroll and fixed layouts where the view cannot scroll (a floating or fullscreen window, fixed overflow, or a named workspace), a quick swipe runs `focus-column-left/right` or `focus-workspace-prev/next` when the fingers lift. Cascade keeps horizontal workspace navigation; vertical band swipes do nothing while a floating or fullscreen window has focus.

Two-finger scroll goes to the window under the pointer with the touchpad's timestamps, so apps with kinetic scrolling keep their inertia.

## HDR

`output.<name>.hdr = on` sends HDR10 (BT.2020, PQ) to displays whose EDID and connector support it. The desktop stays SDR content, shown at `sdr-brightness` nits.

HDR-aware Wayland clients, including games through Vulkan WSI, browsers, and video players, can present HDR through `wp_color_manager_v1` and `wp_color_representation_v1`. Some apps require HDR to be enabled in their own settings. X11 clients stay SDR.

Hardware-decoded video can use NV12 (8-bit) or P010 (10-bit) DMA-BUF subsurfaces with BT.601/709/2020 coefficients, full or limited range, and chroma-location type 0. The Vulkan device must support sampling the format/modifier with per-plane views; planes in one DMA-BUF use one memory import, while separate plane buffers additionally require Vulkan DISJOINT support. Unsupported formats are not offered. Confirm hardware decoding and the advertised DMA-BUF formats when testing an app.

- Fullscreen HDR buffers (10-bit, PQ) go straight to the display unchanged.
- Opaque SDR (sRGB) buffers also skip composition on HDR outputs when the KMS plane has a colour pipeline (`DRM_CLIENT_CAP_PLANE_COLOR_PIPELINE`): sRGB curve, `sdr-brightness`, BT.709 to BT.2020, PQ, the same maths as composition. This applies to a fullscreen window (direct scanout) and to a lone window on an overlay plane. The compositor looks for that chain on each plane, whatever the driver; amdgpu on recent kernels has it. Without it, with alpha, with YUV or extended-linear content, SDR windows are composed, and the log gives the reason (`hdr_sdr_content`, `yuv`). When the driver refuses a pipeline (tested once per plane, buffer format and cursor state), the window is composed with reason `color_refused`.
- Composed frames and every modeset leave the planes' pipeline on Bypass.
- A VT switch resets the pipeline first: before the seat is disabled, each output that applies a pipeline commits Bypass (at most 100 ms are spent waiting for it, then the switch goes on and a warning is logged). The pipeline survives a master change, and a client without `DRM_CLIENT_CAP_PLANE_COLOR_PIPELINE` cannot clear it. A crash or `kill -9` cannot do this reset: the next DRM master inherits the pipeline, with its colours distorted, until a client that sets the cap commits a modeset (or this compositor starts again).
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
- `spawn <command>` runs a program without a shell; `spawn a; b` runs several. See [Running commands](desktop.md#running-commands).

### Cascade

Set `layout.overflow = cascade` globally, per output, or on a named workspace. Columns fill bands of up to `max-columns`, then continue in a fresh band below. The columns of a band share its width equally, as in `fixed` overflow: a lone column takes the full width until another joins its band. New columns append last; closing a column compacts the following bands. Each band fills the usable height after panel reservations.

Left/right focus follows column order across band boundaries. Up/down visits stacked windows first, then the closest column in the adjacent band. Column width presets and resizing do nothing; window height shares still apply. Maximizing a column hides its same-band siblings until focus moves or maximization ends.

Three-finger vertical swipes scroll bands; horizontal swipes switch workspaces. Scroll and fixed workspaces retain their existing gesture axes. A switch keeps the source workspace's axis throughout its transition; the destination's axes apply to the next gesture. The `focus-workspace-prev/next` commands switch to the previous or next workspace, whether the transition is vertical or horizontal. Four-finger and stash gestures keep their existing behaviour.

In the [overview](#overview), the selected band is centred at the usual overview zoom, whatever the band count. The other bands of the workspace sit above and below it, dimmed, and may run off screen. Neighbor workspaces sit left and right, fully dimmed, with their selected row level with the centred band. The stash pile stays left of the band, inside the workspace's slot. A maximized column shows the columns it hides as dimmed cards behind it, peeking out on their side of the band: the nearest one on each side, then the next ones stacked at the band's edge. Selecting a card (left/right, Return or a click on a visible card) moves the maximization to it, and Escape gives it back.

In the overview, up/down move to the previous or next band and stop at the first and last; with a stash card selected they browse the pile. Left/right move along the band; past its first column they enter the stash, or go to the previous workspace when there is none; past its last column they go to the next workspace (with the overview closed, focus crosses into the next band instead). Covering floats fan left and right of the band: left/right traverse them before entering the stash or changing workspace, and up/down do nothing on a float. `h`/`j`/`k`/`l` and the focus binds follow the arrows; `focus-workspace-prev/next` always change workspace, except over a selected stash card, where they browse the pile. Three-finger swipes follow the fingers: vertical moves one band (or one stash card) and horizontal one workspace per gesture, even over a stash card. Over a maximized column that hides cards, horizontal moves one step per gesture like left/right: the next card, then past the band's edge the stash or a workspace. A vertical wheel or continuous scroll changes workspace, except over a selected stash card, where it browses the pile; a horizontal one moves along the band.

Cascade supports dropping onto tiles, but not gap insertion or edge scrolling during a drag.

### Actions

| Action | Effect |
|---|---|
| `spawn <command>` | Run a program |
| `spawn-terminal` | Run `terminal` |
| `close-window` | Close the focused window |
| `toggle-fullscreen` | Fullscreen the focused window. An app that asks for fullscreen in its first second (e.g. a Wine launcher that remembered a monitor-sized window) opens in a column instead; later requests are honoured. Fullscreen is exclusive, whether from this bind or the app: nothing is drawn above it but a surface taking the keyboard (a locker). Bars, notifications and other windows wait hidden until it leaves; a window opening meanwhile waits hidden too. The window's own dialogs are the exception: a dialog it opens (a file chooser, a screen-sharing or permission request from the desktop portal) shows over it with the focus, and a focus move goes back to the fullscreen window without leaving fullscreen. Fullscreen stays in place: the window keeps its column. Focusing another window of the workspace (`focus-column-left/right`, `focus-window-up/down`, an activation or the overview) leaves fullscreen; the app's own fullscreen requests are then refused until it is focused again. In fixed overflow, an app's own request applies only while it has the focus. A taskbar request brings the window on screen with the focus first |
| `toggle-window-stash` | Move the focused tile to the end of the workspace's [stash](#stash), shown and selected; on a stashed window, send it back to its former column, width and maximized or expanded state when possible. A native dialog becomes a new tiled column. A fullscreen window must leave fullscreen first |
| `toggle-floating` | Make the focused tile a [free floating window](#free-floating-windows) at its current size, centred; on a floating window, send it back to its former place in the columns (a native dialog becomes a new column). Does nothing on a stashed or fullscreen window |
| `toggle-stash-visible` | Hide the stash and give the focus back to the tiles, or show it again with the focus on its selected window. Native dialogs stay as they are. Over a fullscreen window, shows the stash on top with the focus; the window stays fullscreen behind (composed, so no direct scanout while the stash shows) and is back when the stash hides or the focus leaves it. `toggle-window-stash` or `toggle-fullscreen` on a stashed window there leaves that fullscreen for it. New windows never join or show over it; the fullscreen window's own dialogs show above it |
| `toggle-overview` | Open the [overview](#overview), or close it on the selected window |
| `maximize-column` | Toggle full usable width for the focused column, preserving gaps and its saved width; in fixed overflow, other columns are hidden until focus moves or it is toggled off. On a window that made itself fullscreen (e.g. a Wine app at monitor size), it first returns the window to its column |
| `cycle-column-width` | Step through `layout.presets`. In fixed overflow, toggle the focused column to `max-columns - 1` cells in place; the other columns stack on each side in the last cell. One column is expanded at a time |
| `focus-column-left/right` | Focus the neighbor column; at the edge, the neighbor monitor. In fixed overflow, the neighbor on screen. From a fullscreen window, the neighbor column comes first and fullscreen ends; at the edge it stays |
| `focus-window-up/down` | Focus in the column; past the edge, the next workspace. In fixed overflow, the column on screen above or below comes first. Up at the top brings back a covering float put behind the columns. From a fullscreen window, a window above or below comes first and fullscreen ends |
| `move-column-left/right` | Move the focused column |
| `move-window-up/down` | Move the focused window up or down in its column; focus follows it |
| `set-column-width <+N%\|-N%>` | Widen or narrow the focused column by N% of the usable width (1-100), between 10% and 100%. The new width stays until changed; `cycle-column-width` then restarts at the first preset. Does nothing in fixed overflow |
| `set-window-height <+N%\|-N%>` | Give the focused window N% more or less of its column's height; the other rows give or take it in proportion. Each row keeps at least 10%. A window joining or leaving the column resets equal rows. Does nothing past 9 rows |
| `consume-or-expel-window-left/right` | A lone window joins the neighbor column; a stacked one leaves for a new column |
| `focus-workspace <N>` / `focus-workspace-prev/next` | Show a numbered or neighbor workspace |
| `workspace <name>` | Toggle a named workspace |
| `move-column-to-workspace <N>` / `-prev/-next` | Move the focused column |
| `move-window-to-workspace <N>` / `-prev/-next` | Move only the focused window |
| `focus-monitor-left/right` / `focus-monitor-up/down` | Focus the neighbor monitor in that direction |
| `move-workspace-to-monitor-left/right` / `move-workspace-to-monitor-up/down` | Move the workspace to the neighbor monitor in that direction; it gets a new home |
| `move-workspace-prev/next` | Swap the current numbered workspace with its neighbor; the view follows it. It never passes the empty workspace at the end |
| `scale-up` / `scale-down` | Zoom the whole display; one second after the last press, the scale is saved to `output.<name>.scale` and a notification confirms it (`notify-send`) |
| `quit` | Exit NeferWL |

### Stash

Each workspace has a stash: a horizontal strip of windows set aside with `toggle-window-stash`, in the order they arrived. The selected window is centred over the tiles, `stash.width` of the usable width (80% by default) and 80% of its height. Its left and right neighbors sit `stash.gap` beside it and show up to the screen edges, dimmed (`stash.dim`); a click on one selects it. The others wait off screen.

While the stash has the focus, `focus-column-left/right` and the three-finger swipe move through it, sliding the view, and stop at its ends; `focus-window-up/down` do nothing. Hide it with `toggle-stash-visible`, or with a four-finger swipe down or up, to get back to the tiles; a swipe down shows it again. Scripts see each stashed window's place, and whether the stash is hidden, in the [state file](desktop.md#state-for-scripts).

Native floating windows are not in the stash: they stay centred in the usable output area. A window-sized float that fills that area (within two border widths plus two logical pixels per axis) stays below the columns when you focus a tile; `focus-window-up` at the top of a column raises it again. It does not hide bars or pin focus. Small dialogs and file pickers always stay above the columns; shrinking a float promotes it above them. Real fullscreen remains exclusive, except for the fullscreen window's own dialogs. A dialog belongs to a window through `xdg_toplevel.set_parent`, or through `zxdg_importer_v2` when another program opens it, as the desktop portal does.

### Free floating windows

`toggle-floating` lifts the focused tile out of its column into a free floating window. NeferWL sets its size, and it stays where you put it, kept inside the usable area. Its place is relative to the screen, so it keeps it on another workspace or monitor of any size.

On a free floating window, `move-column-left/right` and `move-window-up/down` move it by 50 logical pixels, and `set-column-width`/`set-window-height` resize it by a share of the usable width or height. `toggle-window-stash` stashes it and brings it back floating at the same place. `toggle-floating` returns it to its column.

### Moving windows with the pointer

Hold `cmd` and drag with the left button to move the window under the pointer; drag with the right button to resize a floating window from its bottom-right corner. Apps that drag their own title bar or edges (`xdg_toplevel.move`/`resize`) do the same without `cmd`. The window gets no pointer events while you drag, and `Escape` cancels the drag.

A floating window follows the pointer and becomes a [free floating window](#free-floating-windows). A tile stays in place until you release the button. Until then, a bar or frame in `border.active` color shows where it will land:

- top or bottom quarter of a window: stack it above or below that window;
- middle of a window: swap the two columns (a window from a stacked column becomes a new column there);
- in scroll overflow, the gap between two columns: a new column there. Moving the pointer against the left or right edge scrolls the workspace one column at a time.

The drop can be on another monitor; focus follows the window. There, the middle of a window puts it in a new column at that place: only columns of the same workspace swap. A full `fixed` workspace has no room for a new column, so the window stacks in the outlined one.

### Overview

`toggle-overview` shows scaled previews of the current workspace, including over fullscreen windows, over a background darkened by `floating.dim`. Windows keep their size and previews show their last frames. Covering floats and windows hidden behind a maximized column in `fixed` overflow appear as cards: the on-screen item is in front, hidden columns share one spiral-layout card, and in a vertical overview up to two cards behind peek above it and passed cards peek below (a Cascade overview fans them left and right). Neighbor workspaces show dimmed stacks. Each monitor has one overview, laid out along the navigation axis of the current workspace's layout: neighbors appear above and below a `scroll` or `fixed` workspace and left and right of a `cascade` one (see [Cascade](#cascade)); stepping to a workspace with another layout turns the overview. A named workspace appears directly below the numbered workspace from which its `workspace <name>` bind was invoked. Invoking it from another numbered workspace updates this placement; browsing the overview does not. Several named workspaces attached to the same numbered workspace follow configuration order, with rules across the axis (horizontal in a vertical overview, vertical in a side-by-side one) separating their group from numbered rows. Named-to-named binds reuse the source's numbered attachment. Unattached named workspaces follow the last occupied numbered row, or the first row if all numbered workspaces are empty. The selected workspace stays centred and its immediate neighbors appear beside it along that axis; in a vertical overview, rows and stash previews keep their full width. Empty workspaces are skipped unless currently selected or serving as the invocation anchor of a visible named row. Named workspaces remain outside the numbered list and normal previous/next workspace navigation. Smaller floats, such as dialogs, stay hidden. A fullscreen floating window, or any fullscreen window with `fixed` overflow, is its workspace's front card, with the windows it hides behind it; `scroll` overflow keeps its columns selectable in one card. Choosing the fullscreen window keeps it fullscreen; choosing another window leaves fullscreen.

| Key | Action |
| --- | --- |
| `h` / `l`, `left` / `right` | Move within a front column-group card; left of its first tile enters the stash. Left on a single-window card sends it behind, or enters the stash when none is behind; right does nothing on a single card. |
| `k` / `j`, `up` / `down` | Browse cards within the selected stash pile; otherwise bring the next card above / previous card below to the front, then change workspace at the stack boundary |
| `return` | Show the selected front card; picking a hidden column moves the maximization to it. Other rows shown keep their focus, maximization and float order |
| `escape` | Return to the original workspace; every row shown keeps its focus, maximization and float order |

The focus binds (`focus-column-left/right`, `focus-window-up/down`, `cmd+arrows` by default) move the selection like these keys. In the overview, `focus-workspace-prev/next` also traverse cards before crossing workspace rows, including named ones, except inside a selected stash pile. The keys above describe vertical overviews; in a [Cascade](#cascade) overview they move as described there.

Each workspace's [stash](#stash), hidden or not, shows as a pile of cards on the left of its row, which stays centred unless it would overlap the pile (in a Cascade overview the pile extends its workspace's slot to the left): its selected window in front, up to three others behind it, dimmed. In the pile, `k` / `j` or up/down browse stash cards and stop at either end without changing workspace. In a vertical overview, right (`l`) leaves the pile immediately, left (`h`) stays in the pile, and on a stash-only workspace right deselects the pile so up/down can change workspace. In a Cascade overview, left from the pile goes to the previous workspace, right returns to the band's first column (or to the next workspace on a stash-only workspace), and up/down browse the pile. Browsing brings the selected card to the front without reordering the stash. `return` or a click on a card closes the overview with the stash shown on that window.

A four-finger swipe up opens the overview, or hides the [stash](#stash) when it is shown, and a swipe down closes the overview on the selection (with the overview closed, a swipe down shows or hides the stash), whatever `touchpad.natural-scroll` says. Two-finger scrolling belongs to applications and never navigates the overview; previews do not accept application input. In a vertical overview, three fingers move the selection during the gesture, following `touchpad.natural-scroll`: horizontally, a short distance selects the next app and further movement repeats at a longer distance; vertically, one step per gesture selects a stack card or changes workspace. Inside a selected stash pile, vertical movement browses its cards and horizontal movement enters or leaves the pile. Lifting resets the gesture without an extra step; cancellation keeps selections already made. A Cascade overview follows the fingers instead (see [Cascade](#cascade)). The mouse wheel moves one step per notch, following `mouse.natural-scroll`. Three-finger navigation, the wheel and keyboard navigation share the same pile boundaries.

Return or a click on a front tile or peeking card commits it; card changes remain provisional until then. `close-window` targets the selected preview. Move-to-workspace binds (`move-column-to-workspace*`, `move-window-to-workspace*`) move the selected preview at once and keep the overview open; with `focus.follow-move`, the selection follows it. Escape does not undo a move, and the row that received the window keeps it focused. A selected stash card, or a row with no preview, does not move. Other window mutation binds (moving within the row, resizing, maximizing, fullscreen and stash toggles) and moving a workspace to another monitor are disabled while the overview is open. Workspace and monitor navigation, launch and quit binds remain active.

### Default binds

With `keyboard.cmd = alt`, the `cmd+alt+…` binds have the same keys as `cmd+…` and are dropped with a warning; with `keyboard.cmd = ctrl`, so are the `cmd+ctrl+…` ones. Bind those actions to other keys.

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
| `cmd+shift+up` / `cmd+shift+k` | `move-window-up` |
| `cmd+shift+down` / `cmd+shift+j` | `move-window-down` |
| `cmd+alt+left` / `cmd+alt+h` | `set-column-width -10%` |
| `cmd+alt+right` / `cmd+alt+l` | `set-column-width +10%` |
| `cmd+alt+up` / `cmd+alt+k` | `set-window-height -10%` |
| `cmd+alt+down` / `cmd+alt+j` | `set-window-height +10%` |
| `cmd+r` | `cycle-column-width` |
| `cmd+f` | `maximize-column` |
| `cmd+shift+f` | `toggle-fullscreen` |
| `cmd+s` | `toggle-stash-visible` |
| `cmd+shift+s` | `toggle-window-stash` |
| `cmd+shift+space` | `toggle-floating` |
| `cmd+o` | `toggle-overview` |
| `cmd+q` | `close-window` |
| `ctrl+alt+backspace` | `quit` |
| `ctrl+cmd+space` | `spawn fuzzel` |
| `cmd+pageup` | `focus-workspace-prev` |
| `cmd+pagedown` | `focus-workspace-next` |
| `cmd+shift+pageup` | `move-column-to-workspace-prev` |
| `cmd+shift+pagedown` | `move-column-to-workspace-next` |
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
| `cmd+ctrl+up` / `cmd+ctrl+k` | `focus-monitor-up` |
| `cmd+ctrl+down` / `cmd+ctrl+j` | `focus-monitor-down` |
| `cmd+ctrl+h` | `focus-monitor-left` |
| `cmd+ctrl+l` | `focus-monitor-right` |
| `cmd+ctrl+shift+left` | `move-workspace-to-monitor-left` |
| `cmd+ctrl+shift+right` | `move-workspace-to-monitor-right` |
| `cmd+ctrl+shift+h` | `move-workspace-to-monitor-left` |
| `cmd+ctrl+shift+l` | `move-workspace-to-monitor-right` |
| `cmd+ctrl+shift+up` / `cmd+ctrl+shift+k` | `move-workspace-prev` |
| `cmd+ctrl+shift+down` / `cmd+ctrl+shift+j` | `move-workspace-next` |
| `cmd+code:13` | `scale-up` |
| `cmd+code:12` | `scale-down` |
