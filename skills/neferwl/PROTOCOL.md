# Control protocol

For clients that talk to the socket directly. Full contract: `docs/dev/control.md` in the NeferWL repo.

Connect to `$XDG_RUNTIME_DIR/neferwl/sessions/<id>/control.sock` (Unix stream, same uid only). Send one JSON object per line; each request gets one reply line, in order. `id` is optional and echoed (`null` when missing or the line is not JSON). Unknown fields are ignored; tolerate new reply fields.

```sh
printf '%s\n' '{"id":1,"op":"hello"}' | socat - UNIX-CONNECT:"$XDG_RUNTIME_DIR/neferwl/sessions/test/control.sock"
```

## Requests

| `op` | Fields | Notes |
|---|---|---|
| `hello` | | `{"ok":true,"v":1,"caps":[...],"commands":[{name,arg,kind,caps,summary}]}` |
| `query` | `what: "state"` | reply `state` |
| `do` | `action` | catalog name plus args in one string: `"focus-workspace 2"`, `"spawn foot --app-id x"` |
| `inject` | `kind` + fields below | goes through the core input path, so binds fire |
| `shot` | `outputs` (`[]` = all), `inline`, `timeout` | `raw: true` is refused |
| `shot` | `every_frame: true` or `stop: true`, `outputs`, `dir` (absolute), `raw` | continuous capture; reply `dir` |
| `output-add` | `mode: "WxH[@Hz]"`, `name` (optional) | |
| `output-rm` | `name` | runtime outputs only |

`inject` kinds:

| `kind` | Fields |
|---|---|
| `key`, `press`, `release` | `combo: "Super+Return"` |
| `type` | `text` |
| `pointer` | `cmd: "move"`, `args: ["x","y"]` (strings, global logical); `cmd: "click"\|"down"\|"up"`, `args: ["left"\|"right"\|"middle"]` (`click` alone = left; `down`/`up` need a button) |
| `swipe` | `fingers: 3\|4`, `dir: "up"\|"down"\|"left"\|"right"` |

`do`, `inject` and single `shot` also take `wait` (`committed`, `settled` default, `presented`), `until` (`"window-mapped app_id=foot"`, `"focused app_id=foot"`, `"unmapped id=3"`) and `timeout` (Go duration, default `10s`, max `5m`; larger gives `bad-request` with `detail`). `do` and `inject` take `shot: true` or `shot: ["HEADLESS-1"]`, which forces `presented`.

```json
{"id":1,"op":"inject","kind":"key","combo":"Super+Return","until":"window-mapped app_id=foot","timeout":"5s"}
{"id":2,"op":"inject","kind":"pointer","cmd":"move","args":["320","240"]}
{"id":3,"op":"do","action":"toggle-overview","wait":"presented","shot":true}
{"id":4,"op":"shot","outputs":["HEADLESS-1"],"inline":true}
{"id":5,"op":"output-add","mode":"800x600@60","name":"extra"}
```

## Reply

```json
{"id":3,"ok":true,"done":"presented","diff":{"workspaces":["HEADLESS-1:1"]},"state":{...},"late":null,
 "shots":[{"output":"HEADLESS-1","path":".../shots/2239-HEADLESS-1.png","seq":2239}]}
```

`error` (code) and `detail` appear on failure; `shots` and `dir` only when relevant. Field meanings and error codes: [CLI.md](CLI.md#reply). On `timeout` the reply still carries `diff`, `state` and `late`. While the session is locked, input, captures and actions other than `quit` get `locked`.
