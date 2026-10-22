# Built-in launcher verification (Phase 5)

Verified on 2026-10-22 with Go 1.27.3 and Xwayland 24.1.14-1.
The launcher uses only the decided `:N -rootless -listenfd ... -wm ...
-displayfd ...` arguments (no `-terminate`). `/usr/sbin/Xwayland -help`
confirms each flag. No XWM or global filter is implemented.

## Headless evidence

Built using `CGO_ENABLED=0 go build -o /tmp/xwb/neferwl ./cmd/neferwl`.
Both runs used `env -u NEFERWL_STATE -u NEFERWL_SLOT`, throwaway configs,
separate `XDG_STATE_HOME` directories, `--backend=headless --no-terminal
--size 640x360`, and bounded `--timeout` shutdowns. Neither used
`--no-xwayland`.

Built-in (`xwayland = builtin`):

- Reserved `DISPLAY=:1`, Wayland socket `wayland-2` at 08:16:55.
- No Xwayland process started before the X client connected at 08:16:57.
- Logged `Xwayland started` (pid 160850), `Xwayland attached`, and
  `Xwayland ready` at 08:16:57.
- **Unmet gate:** `DISPLAY=:1 timeout 5s xprop -root` timed out (124),
  without output. A second run with debug logging and strace reproduced
  the timeout despite readiness. No fatal Xwayland error was logged.
  Root cause is not established; the WM socket is intentionally held open
  without an X11 handshake or requests in this phase. Do not treat the
  readiness signal as proof that external X11 clients work without an XWM.
- Compositor exited 0 on timeout. Before and after `pgrep -a Xwayland`
  contained only the user's existing pid 4467 on `:0`.

Default satellite (config with no xwayland key):

- Reserved `:1`, started satellite pid 175585 only after the client connected.
- `DISPLAY=:1 timeout 4s xprop -root` exited 0, printing `_NET_SUPPORTED`,
  `_NET_ACTIVE_WINDOW`, `_NET_SUPPORTING_WM_CHECK`, `_XKB_RULES_NAMES`.
- Compositor exited 0; only pre-existing Xwayland pid 4467 remained.

Raw local artifacts: `/tmp/xwb/{builtin-state,satellite-state}/neferwl/runs/headless/`,
`builtin-xprop.txt`, `satellite-xprop.txt`, `before.txt`, `after.txt`, `trace`.

## Phase 6 handoff

`Server.xwayland` is display-owner state. It records the adopted Wayland
client and holds the WM socket until replacement, client destruction or
server shutdown. Phase 6 takes this fd for the XWM. `AttachXwayland` owns
both fds on call, including failure and stopped-display paths. The launcher
duplicates parent socket descriptors before handing them off, so `os.File`
cleanup/finalizers cannot close descriptors owned by libwayland/the server.

The external-client timeout needs resolution before declaring the full
Phase 5 acceptance gate satisfied; no extra flags or partial XWM were added
to bypass the decided boundary.
