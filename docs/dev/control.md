# Control protocol

Wire reference for the headless control socket. For sessions, targeting and CLI
usage, see [Scripting and agents](../control.md).

## Connection

Each session listens on `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/control.sock`
(directory `0700`, socket `0600`). The server reads the peer's credentials with
`SO_PEERCRED` and closes connections from another uid. An accepted connection
has every capability: `read`, `act`, `input`, `capture`, `admin`.

Messages are JSON lines: one object per line, in both directions. A connection
handles one request at a time: the next line is read after the previous reply
is sent. Open several connections for concurrent requests.

`id` is optional and may be any JSON value; the reply echoes it, or carries
`null` when it is missing or the line is not valid JSON.

## Handshake

```json
{"id":1,"op":"hello"}
{"id":1,"ok":true,"v":1,"caps":["read","act","input","capture","admin"],"commands":[{"name":"output-add","arg":"MODE","kind":"admin","caps":["admin"],"summary":"Add a runtime virtual output."},...]}
```

`commands` is the catalog: each entry has `name`, `arg`, `kind` (`action`,
`action-arg`, `query`, `inject`, `shot`, `admin`), `caps` and `summary`. Action
names are the bind actions; `do` has no automation-only names. Additive changes
keep `v: 1`; clients ignore unknown fields.

## Requests

| `op` | Fields | Capability |
|---|---|---|
| `do` | `action` (name and arguments, e.g. `"focus-workspace 2"`), wait fields | `act` |
| `inject` | `kind` and its fields (below), wait fields | `input` |
| `query` | `what`: `"state"` | `read` |
| `shot` | `outputs`, `inline`, `raw`, `timeout` | `capture` |
| `shot` (continuous) | `every_frame` or `stop`, `outputs`, `dir`, `raw` | `capture` |
| `output-add` | `mode` (`WxH[@Hz]`), optional `name` | `admin` |
| `output-rm` | `name` | `admin` |

Wait fields, on `do` and `inject`:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `wait` | `"committed"`, `"settled"`, `"presented"` | `"settled"` | Completion level |
| `until` | string | none | State predicate |
| `timeout` | Go duration string | `"10s"` | Bound on the wait, at most `5m` |
| `shot` | `true`, `false` or a list of output names | none | Capture after the wait; implies `presented` |

### Inject kinds

| `kind` | Fields | Meaning |
|---|---|---|
| `key` | `combo` | Press and release a combination such as `Super+Return` |
| `type` | `text` | Type text through the session keymap |
| `press`, `release` | `combo` | Press, or release in reverse order |
| `pointer` | `cmd`, `args` | `move` with absolute global logical `x`, `y` strings; `click` (default `left`), `down`, `up` with `left`, `right` or `middle` |
| `swipe` | `fingers` (3 or 4), `dir` | Touchpad swipe `up`, `down`, `left` or `right` |

```json
{"id":1,"op":"inject","kind":"key","combo":"Super+Return","until":"window-mapped app_id=foot"}
{"id":2,"op":"inject","kind":"pointer","cmd":"move","args":["320","240"]}
{"id":3,"op":"inject","kind":"swipe","fingers":4,"dir":"down","wait":"presented"}
```

Injected input goes through the core input path, so compositor binds run as
for a physical device. The first injection on a connection announces an input
device with ID `1` named `control` (for `output.<name>.input = control`).

## Wait semantics

- `committed`: the action or input batch has been applied to core state.
- `settled`: animations are finished and every affected window has committed
  its configured size.
- `presented`: the resulting scene has been presented on its outputs.

`until` is checked on top of the level; the reply comes when both hold.

| Predicate | Holds when |
|---|---|
| `window-mapped app_id=APP` | A window with that app ID mapped after the request |
| `focused app_id=APP` | A window with that app ID has focus |
| `unmapped id=N` | Window `N` is gone |

`timeout` covers the wait and any capture together. A `timeout` above `5m` is
`bad-request` with `detail: "timeout exceeds 5m"`. On timeout the reply has
`error: "timeout"` and still carries `diff`, `state` and `late`: the action may
have run.

At most 64 requests wait at once per session (any request with `wait` above
`committed`, `until`, `shot`, `output-add` or `output-rm`). A further one gets
`busy` before it runs. Committed-only requests and `quit` are never refused for
this.

## Replies

```json
{"id":2,"ok":true,"done":"settled","diff":{"mapped":[2],"focus_from":1,"focus_to":2,"focus_changed":true},"state":{...},"late":null}
```

| Field | Meaning |
|---|---|
| `id` | Echo of the request `id` |
| `ok` | `true` on success |
| `done` | Level reached: `committed`, `settled`, `presented` |
| `diff` | Changes since the request: `mapped`, `unmapped` (window IDs), `focus_from`, `focus_to`, `focus_changed`, `workspaces` (`"OUTPUT:WS"` for each output whose shown workspace changed). Unchanged parts are omitted. |
| `state` | State after the request, in the state file's shape, without window titles |
| `late` | Window IDs still not settled at timeout |
| `shots` | Captures: `{output, path, seq, png?}` |
| `dir` | Continuous capture directory |
| `error` | Error code, omitted on success |
| `detail` | Human-readable detail for some errors |

## Captures

```json
{"id":4,"op":"shot","outputs":["HEADLESS-1"],"inline":true}
{"id":4,"ok":true,"done":"presented","shots":[{"output":"HEADLESS-1","path":"/run/user/1000/neferwl/sessions/demo/shots/7-HEADLESS-1.png","seq":7,"png":"iVBOR..."}],...}
```

An empty or missing `outputs` selects every output; an unknown name is
`bad-request`. Shots wait for presentation and write private 8-bit sRGB PNG
files under the session's `shots/` directory. `seq` is the presented scene
sequence. `png` (base64) is present only with `inline: true`; the file is
written anyway. `raw: true` on a single shot is `bad-request` with
`detail: "raw shots need --every-frame"`.

Continuous capture:

```json
{"id":5,"op":"shot","every_frame":true,"dir":"/tmp/frames","outputs":["HEADLESS-1"],"raw":false}
{"id":6,"op":"shot","stop":true}
```

`dir` must be absolute; it defaults to the session's `shots/frames`. With
several outputs, each writes to `<dir>/<output>/`. Each directory gets
`frame-NNNNNN.png`, `latest.png` (hard link to the newest frame) and
`frames.ffconcat`; `raw: true` adds `latest-pq.png` on HDR outputs. Frames are
skipped while the session is locked. `every_frame` with `stop`, or with
`inline`, is `bad-request`.

## Runtime outputs

```json
{"id":7,"op":"output-add","mode":"800x600@60","name":"extra"}
{"id":8,"op":"output-rm","name":"extra"}
```

The reply comes once core state lists or drops the output, within 10 seconds.

## Errors

| Code | Meaning |
|---|---|
| `bad-request` | Invalid JSON, unknown `op`, or invalid fields |
| `unknown-command` | `action` is not in the catalog |
| `forbidden` | The connection lacks the capability |
| `locked` | The session is locked: actions other than `quit`, input and captures are refused |
| `busy` | 64 requests are already waiting, or the capture backend is busy; retry later |
| `timeout` | The completion condition was not reached in time; partial results are included |
| `capture-failed` | A capture or the frame writer failed; see `detail` |

## Session startup

`neferwl session new` starts a detached child with stdin, stdout and stderr on
`/dev/null`:

```text
<executable> --backend=headless --profile=P --session-id=ID --ready-fd=3
             [--size=WxH,...] [--config=PATH] [--timeout=TTL] [extra flags...]
```

The child writes one JSON line with `id`, `wayland`, `control` and `display`
to fd 3 when ready, then closes it. EOF before that line means startup failed.
The child environment drops `WAYLAND_DISPLAY`, `WAYLAND_SOCKET`, `DISPLAY`,
`NEFERWL_SESSION`, `NEFERWL_CONTROL`, `NEFERWL_STATE` and `NEFERWL_SLOT`, and
`DBUS_SESSION_BUS_ADDRESS` unless `--dbus-session` is given.

Session metadata lives in `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/meta.json`
beside `control.sock`, `state.json`, `shots/` and a lifetime `lock`.
`session ls` reclaims unlocked stale directories. `session rm` sends
`{"op":"do","action":"quit","wait":"committed"}` and waits for the lock to be
released; if the socket is unreachable, it sends SIGTERM only after checking
that the recorded PID runs the same executable.
