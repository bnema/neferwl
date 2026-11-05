# neferwl CLI reference

`neferwl <verb> --help` is authoritative. Control verbs print the JSON reply on stdout (indented). Exit codes: 0 ok; 1 error reply or runtime failure (code or message on stderr); 2 usage error (message plus `usage:` line on stderr).

## Target

| Source | Notes |
|---|---|
| `-s <id>` | before the verb (`neferwl -s test do none`, `-s=test`) or among its flags; rejected by verbs without control (`session`, `version`) |
| `NEFERWL_SESSION` | session ID; set in processes the session launches |
| `NEFERWL_CONTROL` | socket path; used only when no ID is given |
| `WAYLAND_DISPLAY` | the live session whose `wayland` matches |

An unresolved target fails and lists the live sessions. `state` alone also falls back to `$NEFERWL_STATE`, then the state file of `$WAYLAND_DISPLAY`: that is the live desktop, so always pass `-s`.

## Sessions

| Command | Notes |
|---|---|
| `session new [flags] [-- compositor flags]` | starts a detached headless session, prints readiness JSON `{id, wayland, control, display}` |
| `session ls [--json]` | live sessions in ID order: `ID PROFILE PID WAYLAND CREATED`; reclaims stale directories |
| `session rm [--timeout 30s] <id>` | `do quit` at the committed boundary, then waits for the lifetime lock; SIGTERM only if the socket is unreachable and the PID runs the same executable |

`session new` flags:

| Flag | Default | Notes |
|---|---|---|
| `--id ID` | `s-` + 6 hex | `[a-z0-9][a-z0-9-]*`, max 32 |
| `--profile` | `ephemeral` | `ephemeral`: sized virtual outputs, no physical input, auto terminal. `persistent`: grabs input devices, no fixed outputs |
| `--size WxH[,WxH...]` | one `1920x1080` | one virtual output per size: `HEADLESS-1`, `HEADLESS-2`... |
| `--config PATH` | XDG config | |
| `--ttl D` | none | session exits after D |
| `--start-timeout D` | `30s` | readiness wait |
| `--dbus-session` | off | keep the parent `DBUS_SESSION_BUS_ADDRESS` |
| `--env` | off | print `NEFERWL_SESSION=` and `WAYLAND_DISPLAY=` lines instead of JSON |

The child starts without `NEFERWL_STATE`, `NEFERWL_SLOT`, `NEFERWL_CONTROL`, `NEFERWL_SESSION`, `WAYLAND_DISPLAY`, `WAYLAND_SOCKET` and `DISPLAY`. Useful compositor flags after `--`: `--no-terminal`, `--no-xwayland`, `--debug <components|all>`, `--headless-hdr` (test-only HDR outputs). Never pass `--input-devices grab` in a test: it takes the machine's keyboards.

## Actions and input

| Command | Wire | Notes |
|---|---|---|
| `do <action> [arg...]` | `do` | action name and args joined with spaces; see [Actions](#actions) |
| `key <combo>` | `inject key` | press and release, e.g. `Super+Return`, `Ctrl+Shift+a` |
| `press <combo>` / `release <combo>` | `inject press/release` | release goes in reverse order |
| `type <text>` | `inject type` | one argument; keymap letters and digits, space, newline, ASCII punctuation |
| `click [left\|right\|middle]` | `inject pointer click` | default `left` |
| `move <x> <y>` | `inject pointer move` | absolute global logical coordinates; core clamps to the layout |
| `swipe <3\|4> <up\|down\|left\|right>` | `inject swipe` | touchpad swipe |

Combos: modifiers `Super`, `Shift`, `Ctrl`, `Alt` (exact case), joined by `+`, then one xkb key name (`Return`, `Escape`, `space`, `F1`, `a`, `bracketleft`). A modifier alone is valid (`press Super`). Unknown names give `bad-request`.

Shared flags (before or after arguments; `--` ends flags):

| Flag | Default | Notes |
|---|---|---|
| `-s ID` | | target |
| `--wait L` | `settled` | `committed`, `settled`, `presented` |
| `--until P` | | predicate, see below |
| `--timeout D` | `10s` | > 0 and <= `5m`, else usage error |
| `--shot` | off | capture all outputs; implies `presented` |

Wait levels:

| Level | Reached when |
|---|---|
| `committed` | the action or input is applied |
| `settled` | animations finished and windows committed at their configured sizes |
| `presented` | the resulting scene is presented on the outputs |

`--until` grammar (exactly two words):

| Predicate | Holds when |
|---|---|
| `window-mapped app_id=<id>` | a window with that app ID is mapped that was not mapped when the request started |
| `unmapped id=<n>` | window n (n > 0) existed when the request started and is gone |
| `focused app_id=<id>` | the focused window has that app ID (also if it already had) |

The reply comes when both the wait level and the predicate hold, or at the timeout with `error: "timeout"`.

## State

| Command | Notes |
|---|---|
| `state` | the state object alone (state file shape) |
| `state output-of <pid>` | the output showing that process's window; reads the state file (`$NEFERWL_STATE`), not the socket |
| `query state` | the whole reply, state under `.state` |

State fields: `output` (focused output), `outputs[]` (`name`, `active`, `count`, `workspace`, `workspace_id`, `hdr`), `window` (focused, or null), `windows[]` (`id`, `app_id`, `pid`, `output`, `workspace`, `workspace_id`, `visible`, `floating`, `fullscreen`, `hidden`, `stash_index`, `stash_count`, `column`, `row`, `xwayland`, `sandbox`, `content_type`, `idle_inhibit`, `shortcuts_inhibited`, `pointer_constraint`, `sdr_expansion`), `workspaces[]` (`id`, `number`, `name`, `output`, `visible`, `focused`, `overflow`, `cover`, `columns[]` with `x`, `y`, `width`, `height`, `windows[]` tiles). No titles.

## Capture

| Command | Notes |
|---|---|
| `shot [--output N]... [--inline] [--timeout 10s]` | waits for presentation, writes `shots/<seq>-<output>.png`; `--inline` adds base64 `png` |
| `shot --every-frame [--output N]... [--dir D] [--raw]` | starts recording; default `D` is `shots/frames`; `--dir` is made absolute |
| `shot --stop [--output N]...` | stops recording |

`--output` repeats; none means every output. Unknown output: `bad-request`. `--raw` needs `--every-frame`; `--inline`, `--dir` and `--every-frame`/`--stop` combinations are checked (usage error). Single shots are 8-bit sRGB.

A recording directory, per output (a subdirectory named after each output when several are recorded): `frame-NNNNNN.png`, `latest.png` (the last frame), `frames.ffconcat` (frame durations), and `latest-pq.png` with `--raw` on an HDR output. Video: `ffmpeg -f concat -safe 0 -i frames.ffconcat -c:v libx264 -pix_fmt yuv420p out.mp4`.

## Runtime outputs

| Command | Notes |
|---|---|
| `output add WxH[@Hz] [--name NAME]` | headless only; without a name: next free `HEADLESS-N`; dimensions 1..16384; refresh defaults when `@Hz` is absent |
| `output rm NAME` | only outputs added at runtime; others give `forbidden` |

Both wait until state lists or drops the output (10s deadline).

## Reply

| Field | Notes |
|---|---|
| `id` | echoes the request id (`null` if missing or the line was invalid JSON) |
| `ok` | true on success |
| `done` | wait level reached (`""` for continuous captures and early refusals) |
| `diff` | `mapped`, `unmapped` (window IDs), `focus_from`, `focus_to`, `focus_changed` (0 = no focus), `workspaces` (`output:ws`, name or number); empty parts omitted |
| `state` | state after the request (empty on early refusals) |
| `late` | on timeout, windows still pending; else `null` |
| `shots` | `[{output, path, seq, png?}]` |
| `dir` | continuous capture directory |
| `error` | code, omitted on success |
| `detail` | extra text for some errors, e.g. `timeout exceeds 5m`, `raw shots need --every-frame` |

## Error codes

| Code | Meaning |
|---|---|
| `unknown-command` | action not in the catalog |
| `bad-request` | invalid message, argument, combo, text, output or mode |
| `locked` | session protected: input, captures and actions other than `quit` refused; `query` still works |
| `busy` | 64 requests already waiting (`settled`/`presented`, `--until`, `--shot`, outputs) or the capture backend is busy; retry |
| `forbidden` | operation not allowed, e.g. removing a non-runtime output |
| `timeout` | condition not reached; reply carries partial `diff`, `state`, `late` |
| `capture-failed` | a shot or the frame writer failed |

## Actions

Same names as `bind.<keys> = <action>` in the config. All need the `act` capability.

| Group | Actions |
|---|---|
| Focus | `focus-column-left`, `focus-column-right`, `focus-window-up`, `focus-window-down`, `switch-column-next`, `switch-column-prev` |
| Move | `move-column-left`, `move-column-right`, `move-window-up`, `move-window-down`, `consume-or-expel-window-left`, `consume-or-expel-window-right` |
| Size and mode | `cycle-column-width`, `maximize-column`, `toggle-fullscreen`, `toggle-floating`, `set-column-width +N\|-N` (cells, 1-16), `set-window-height +N%\|-N%` (1-100) |
| Workspaces | `focus-workspace-prev`, `focus-workspace-next`, `focus-workspace N`, `move-column-to-workspace-prev`, `move-column-to-workspace-next`, `move-column-to-workspace N`, `move-window-to-workspace-prev`, `move-window-to-workspace-next`, `move-window-to-workspace N`, `move-workspace-prev`, `move-workspace-next`, `workspace NAME` (toggle named) |
| Monitors | `focus-monitor-left\|right\|up\|down`, `move-workspace-to-monitor-left\|right\|up\|down` |
| Stash and overview | `toggle-window-stash`, `toggle-stash-visible`, `toggle-overview` |
| Launch | `spawn-terminal`, `spawn COMMAND`, `stash COMMAND` (spawn into the stash); commands split on spaces, several separated by `;`, no shell |
| Display | `scale-up`, `scale-down`, `hint`, `osd-pin` |
| Session | `close-window`, `quit`, `none` (no effect; use it to wait or capture) |

The `hello` catalog also lists the non-action commands: `state` (query), `key`, `type`, `pointer`, `swipe` (inject), `shot` (capture), `output-add`, `output-rm` (admin).

## Files

| Path | Content |
|---|---|
| `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/control.sock` | control socket (0600, same uid only) |
| `.../<id>/state.json` | state file |
| `.../<id>/meta.json` | `id`, `wayland`, `display`, `control`, `state`, `profile`, `pid`, `created` |
| `.../<id>/lock` | lifetime lock held by the compositor |
| `.../<id>/shots/` | single shots; `shots/frames/` default recording directory |
| `$XDG_STATE_HOME/neferwl/sessions/<id>/latest.log` | session log (JSON lines), `~/.local/state` when unset |

The runtime directory is removed when the session exits; the log directory stays.
