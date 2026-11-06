---
name: neferwl
description: Explains how to start, drive and observe headless NeferWL sessions through the control socket - session new/ls/rm, targeting, actions, key/text/pointer/swipe input, waits and --until predicates, state queries, screenshots, frame recording and runtime outputs. Use when the user mentions NeferWL, runs `neferwl` commands, tests compositor behavior (binds, gestures, window placement, config keys) end to end, or asks an agent to screenshot or record a Wayland session.
---

# NeferWL headless control

NeferWL is a Wayland compositor. A headless session has no screen: it serves Wayland clients on its own display and listens on a control socket. Each `neferwl` verb below sends one request to that socket, waits for the result, and prints the JSON reply. Every command and flag: [CLI.md](CLI.md). Raw protocol: [PROTOCOL.md](PROTOCOL.md). `neferwl <verb> --help` is authoritative.

## Start a session

```sh
neferwl session new --id test --size 1280x720 --ttl 10m -- --no-xwayland
# {"id":"test","wayland":"wayland-2","control":".../sessions/test/control.sock","display":""}
neferwl session new --id test2 --env      # NEFERWL_SESSION=test2 / WAYLAND_DISPLAY=wayland-3
neferwl session ls                        # ID PROFILE PID WAYLAND CREATED; --json for metadata
neferwl session rm test                   # quit, wait for exit (--timeout 30s, before the ID)
```

- IDs: `a-z`, `0-9`, `-` (not first), 1-32 chars. Without `--id` you get `s-xxxxxx`.
- `--profile ephemeral` (default): virtual outputs from `--size WxH[,WxH...]` (default one 1920x1080), no physical input. `persistent` grabs the machine's input devices: never use it for tests.
- `--config PATH` loads a throwaway config; `--ttl 60s` stops the session after that time. Flags after `--` go to the compositor (`--no-xwayland`, `--no-terminal`, `--debug all`).
- The session opens a terminal on the first workspace (`terminal`, then `$TERMINAL`, then `foot`). Pass `-- --no-terminal` to start empty.
- Reusing a live ID fails with `session startup failed: EOF`. Logs: `$XDG_STATE_HOME/neferwl/sessions/<id>/latest.log`.

## Target

Order: `-s <id>` > `NEFERWL_SESSION` > `NEFERWL_CONTROL` (socket path) > the session owning `WAYLAND_DISPLAY`. Put `-s` before or after the verb. Processes launched inside a session inherit `NEFERWL_SESSION`, `NEFERWL_CONTROL` and `WAYLAND_DISPLAY`, so a script spawned there drives its own session. The systemd server unit runs the persistent session `server`.

## Act, then observe

Each command waits, then prints a reply with `ok`, `done`, `diff`, `state`, `late` and `error`:

```sh
neferwl -s test key Super+Return --until 'window-mapped app_id=foot'
neferwl -s test do focus-column-left              # "diff":{"focus_from":2,"focus_to":1,"focus_changed":true}
neferwl -s test do focus-workspace 2 --wait committed
neferwl -s test do close-window --until 'unmapped id=1'
neferwl -s test type 'echo hi' && neferwl -s test key Return
neferwl -s test move 640 360 && neferwl -s test click right
neferwl -s test swipe 4 down
neferwl -s test press Super && neferwl -s test release Super
```

- `--wait committed|settled|presented`: applied, animations done and windows at their sizes (default), or shown on the output.
- `--until` adds a predicate on top of the wait. `window-mapped app_id=X` needs a window that appeared after the request started; `unmapped id=N` needs window N to have existed then and be gone; `focused app_id=X` only checks current focus.
- `--shot` captures every output once presented and adds `shots` to the reply.
- `--timeout` defaults to 10s, max 5m. On `timeout` the action may still have run: read `diff` and `late` (windows still pending) before retrying.
- `diff`: `mapped`, `unmapped`, `focus_from`/`focus_to`/`focus_changed`, `workspaces` (`output:ws`). Unchanged parts are omitted.
- At most 64 requests wait at once; beyond that the reply is `busy`.
- Action names are the bind actions of the config (`spawn-terminal`, `toggle-overview`, `set-column-width +1`...). List: [CLI.md](CLI.md#actions).

## Observe

```sh
neferwl -s test state | jq '[.windows[] | {id, app_id, workspace, floating}]'
neferwl -s test state | jq -r '.window.app_id'                # focused window
neferwl -s test state | jq '.outputs[] | {name, active, count}'
neferwl -s test query state | jq .state.workspaces            # whole reply, state under .state
```

`state` prints the state object alone; `query state` prints the reply envelope. Window titles are never included.

## Capture

```sh
neferwl -s test shot                                   # every output -> shots[].path
neferwl -s test shot --output HEADLESS-1 --inline | jq -r '.shots[0].png' | base64 -d > out.png
neferwl -s test shot --every-frame --dir /tmp/rec      # one PNG per presented frame
neferwl -s test shot --stop
ffmpeg -f concat -safe 0 -i /tmp/rec/frames.ffconcat -c:v libx264 -pix_fmt yuv420p rec.mp4
```

Single shots go to `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/shots/<seq>-<output>.png`. A recording directory holds `frame-NNNNNN.png`, `latest.png` and `frames.ffconcat`; the default is `shots/frames`, with one subdirectory per output when several are recorded. `--raw` (recording only) adds `latest-pq.png` on HDR outputs.

## Runtime outputs

```sh
neferwl -s test output add 800x600@60 --name extra     # without --name: HEADLESS-2, ...
neferwl -s test output rm extra
```

Only outputs added this way can be removed; removing a `--size` output returns `forbidden`.

## Launch apps into the session

```sh
neferwl -s test do spawn --until 'window-mapped app_id=probe' -- foot --app-id probe -e sleep 60
```

`eval "$(neferwl session new --env)"` sets both variables, but only in a subshell or script: it replaces the caller's `WAYLAND_DISPLAY`. Or let the compositor spawn the app: `neferwl -s test do spawn -- foot --app-id probe`. `spawn` splits on spaces and `;`, without a shell. For X11 apps, set `DISPLAY` to the `display` field of the readiness JSON (empty with `--no-xwayland`). App IDs must match exactly; read them from `state` (weston-terminal is `org.freedesktop.weston.wayland-terminal`).

## Rules

- Never touch the user's live session. Pass `-s <id>` on every command, and never run `neferwl state` without it: with no target it reads the live desktop's state file.
- Remove your sessions with `session rm` and check `session ls`. Never remove `server` or a session you did not create.
- A locked session answers `locked` to input, actions and captures. `query`, `state` and `do quit` still work.
- `--` ends flag parsing for negative coordinates and dashed text: `move -- -640 0`, `type -- -x`. For `do`, put it after the action: `do spawn --until '...' -- foot --app-id x`.
- Key combos: `Super`, `Shift`, `Ctrl`, `Alt` (case-sensitive) plus an xkb key name (`Return`, `a`, `F1`, `bracketleft`). `type` accepts keymap letters and digits, space, newline and ASCII punctuation; anything else is `bad-request`.
- Exit codes: 0 ok, 1 error reply (code on stderr, reply on stdout), 2 usage error. Codes: [CLI.md](CLI.md#error-codes).

## Recipes

Verify a keybind with a throwaway config:

```sh
printf 'bind.cmd+p = toggle-floating\n' > /tmp/nwl.conf
neferwl session new --id kb --config /tmp/nwl.conf --ttl 2m -- --no-xwayland --no-terminal
neferwl -s kb do spawn --until 'window-mapped app_id=foot' -- foot --app-id foot -e sleep 60
neferwl -s kb key Super+p | jq -e '.ok and .state.window.floating'
neferwl session rm kb
```

Wait for an app window, then screenshot:

```sh
neferwl -s test do spawn --until 'window-mapped app_id=probe' --shot -- foot --app-id probe \
  | jq -r '.shots[].path'
```

Record a short video:

```sh
neferwl -s test shot --every-frame --dir /tmp/rec
neferwl -s test do toggle-overview && neferwl -s test do toggle-overview
neferwl -s test shot --stop
ffmpeg -loglevel error -f concat -safe 0 -i /tmp/rec/frames.ffconcat -c:v libx264 -pix_fmt yuv420p /tmp/rec.mp4
```
