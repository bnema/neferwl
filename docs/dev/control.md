# Control socket

Agents and scripts use the control socket to drive headless sessions, run actions
and observe their results without fixed sleeps. The CLI is a client of the same
protocol.

## Connection and target

Each session listens on a Unix socket at
`$XDG_RUNTIME_DIR/neferwl/sessions/<id>/control.sock`. The session directory has
mode `0700`; the socket has mode `0600`. Connections require the same uid as the
owner, which grants all capabilities: `read`, `act`, `input`, `capture`, `admin`.

CLI target resolution uses the first available target, in this order:

1. Explicit `-s <id>`.
2. `NEFERWL_SESSION` (session id).
3. `NEFERWL_CONTROL` (control socket path).
4. The session that owns `WAYLAND_DISPLAY`.

If no target resolves, the CLI reports an error and lists sessions. Use an
explicit target in automation to avoid controlling the desktop by accident.

While the session is protected (locked), input injection and capture are refused
with `locked`, and window titles are masked. Same-uid access does not bypass
this protection.

## Messages

Messages are JSON lines: one JSON object followed by a newline. Requests carry
an `id`, echoed in the reply so clients can correlate requests and responses.
The handshake advertises the protocol version, capabilities and command catalog:

```json
{"op":"hello"}
{"v":1,"caps":[...],"commands":[...]}
```

The ellipses above stand for advertised entries, not literal JSON. Include an
`id` on requests that need correlation.

Additive changes keep `v: 1`. Clients tolerate additional fields and use the
advertised capabilities and commands to discover supported operations.

| Operation | Purpose |
|---|---|
| `hello` | Discover version, capabilities and commands. |
| `do` | Run an action from the bind catalog, with the same name and arguments as a keybind action. |
| `query` | Read `state`, including windows, focus and outputs. |
| `inject` | Send keys, text, pointer or swipe input through the core input path; compositor keybinds work too. |
| `shot` | Capture an output on demand; select the output by name. |

The command catalog is the single source of action names. `do` does not invent a
separate set of automation-only names.

## Input requests

`inject` selects a translator with `kind`:

| `kind` | Fields | Meaning |
|---|---|---|
| `key` | `combo` | Press and release a key combination, such as `Super+Return`. |
| `type` | `text` | Type text through the active keymap. |
| `press`, `release` | `combo` | Press or release a combination. |
| `pointer` | `cmd`, `args` | `move` takes absolute global logical `x`, `y` strings; `click`, `down`, `up` take a button name (`left`, `right`, `middle`). `click` defaults to left. |
| `swipe` | `fingers`, `dir` | Swipe with a finger count and direction (`up`, `down`, `left`, `right`). |

Input requests accept `wait`, `until` and `timeout` like actions:

```json
{"id":1,"op":"inject","kind":"key","combo":"Super+Return","wait":"settled","timeout":"5s"}
{"id":2,"op":"inject","kind":"pointer","cmd":"move","args":["320","240"]}
{"id":3,"op":"inject","kind":"swipe","fingers":4,"dir":"down"}
```

## Act, wait, observe

Actions wait for their effects at one of three levels:

- `committed`: the action is applied.
- `settled` (default): animations finish and windows commit at their configured
  sizes.
- `presented`: the resulting scene is presented on the output.

Use `--wait=committed|settled|presented` to select a level. Attaching `--shot`
implies `presented`, so a capture observes the resulting scene rather than an
older frame.

`--until` adds a typed state predicate:

| Predicate | Condition |
|---|---|
| `window-mapped app_id=…` | A matching application window is mapped. |
| `focused app_id=…` | A matching window has focus. |
| `unmapped id=…` | The identified window is unmapped. |

The predicate is checked on top of the wait level, so the reply comes once
both hold.

`--timeout` bounds the wait. A timeout returns `timeout` with the partial state
diff and any late windows; it does not mean the action had no effect. Inspect
that result before deciding whether to retry.

Replies use this envelope (field names shown, not a literal reply):

```text
{"id", "ok", "done", "diff", "state", "shots", "late", "error"}
```

`id` correlates the reply; `ok` reports success and `done` reports completion.
`diff` describes observed state changes, `state` carries queried state, `shots`
contains capture results, and `late` identifies windows still outstanding.
`error` is omitted on success and describes a failure otherwise. Relevant results, including partial results on
timeout, travel in the same reply.

## Captures and errors

A single-shot request selects output names; an empty `outputs` list selects
all outputs. `timeout` bounds completion:

```json
{"id":4,"op":"shot","outputs":["HEADLESS-1"],"inline":true,"raw":false,"timeout":"5s"}
```

Shots wait for presentation and write private PNG files. The reply’s `shots`
array contains `{output, path, seq, png?}`: `seq` is the presented scene sequence,
and `png` is base64 PNG data included only when `inline` is true. Inline data
does not replace the file. For example:

```json
{"output":"HEADLESS-1","path":"/shots/7-HEADLESS-1.png","seq":7}
```

Select outputs with repeated `--output <name>` flags. Single shots are 8-bit
sRGB; `raw:true` is rejected for them with `raw shots need --every-frame`.
Raw 16-bit PQ captures use the continuous `--every-frame` writer, not the
single-shot CLI flag set.

| Error code | Meaning |
|---|---|
| `unknown-command` | The requested command is not in the catalog. |
| `bad-request` | The message or its arguments are invalid. |
| `locked` | Input or capture is refused while the session is protected. |
| `forbidden` | The connection lacks permission for the operation. |
| `timeout` | The requested completion condition is not reached in time. |

## CLI

`neferwl do <action>` maps to `do`; `neferwl query state` maps to `query`.
`neferwl state` is an alias for `query state`. `neferwl key`, `type`, `press`,
`release`, `click`, `move` and `swipe` map to `inject`; `neferwl shot` maps to
`shot`. `click` and `move` use the pointer injection kind. `neferwl pointer`
also supports the Wayland virtual-pointer workflow described in the
[server guide](../server.md#controlling-a-neferwl-session-agents).
`neferwl session new|ls|rm` creates, lists and removes managed sessions.

```sh
neferwl session new
neferwl -s test query state
neferwl -s test do focus-monitor-right --wait=settled
neferwl -s test shot --output HEADLESS-1 --inline
```

Replace `test` with the id returned by `session new`. For continuous captures,
use `neferwl shot --every-frame --dir <directory>` and stop with
`neferwl shot --stop`; the directory contains PNG frames and `frames.ffconcat`.

## Sessions

`neferwl session ls` lists live sessions in ID order, with profile, PID,
Wayland display and creation time. `--json` prints the registry metadata as a
JSON array. Unlocked stale session directories are reclaimed while listing.

`session new` accepts `--id`, `--profile` (default `ephemeral`), `--size`,
`--config`, `--ttl`, and extra compositor flags after `--`. It starts a detached
child with stdin/stdout/stderr on `/dev/null` using this startup contract:

```text
<executable> --backend=headless --profile=P --session-id=ID --ready-fd=3
             [--size=WxH,...] [--config=PATH] [--timeout=TTL] [extra flags...]
```

The child writes one JSON line containing `id`, `wayland`, `control` and
`display` to fd 3 when ready, then closes it. EOF before that line means startup
failed. The launcher waits at most `--start-timeout` (default `30s`). The child-side
startup flags must be supported by the compositor. By default the launcher
prints the readiness JSON; `--env` (also `--export`) prints `NEFERWL_SESSION`
and `WAYLAND_DISPLAY` assignments. Parent session targeting/display variables
are removed from the child environment. The session bus address is removed
unless `--dbus-session` is given. `session rm` is described below.

`neferwl session rm <id>` asks the selected session to quit at the committed
boundary, then waits for its lifetime lock to be released. `--timeout` before
the ID bounds shutdown (default `30s`). If its control socket cannot be reached,
the command sends SIGTERM only after verifying that the recorded PID runs the
same executable. Stale cleanup is serialized by the registry, so a replacement
live session with the same ID is not removed. No other process is killed.
