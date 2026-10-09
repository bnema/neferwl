# Screen capture

NeferWL supports `zwlr_screencopy_manager_v1` (used by `grim`) and `ext_image_copy_capture_manager_v1` (used by recorders). Three rules apply to every capture:

1. A red indicator shows on screen while something is captured.
2. Only allowed executables may capture.
3. Sandboxed apps go through the desktop portal.

## Sources

With ext-image-copy-capture, a client can capture:

| Source | Protocol | Notes |
|---|---|---|
| An output | `ext_output_image_capture_source_manager_v1` | |
| A window | `ext_foreign_toplevel_image_capture_source_manager_v1` | Rendered at its own size, even on another workspace or under a fullscreen window |
| A workspace | `neferwl_image_capture_source_manager_v1.create_workspace_source` | Its real layout, without bars, even when not on screen |
| A region of an output | `neferwl_image_capture_source_manager_v1.create_output_region_source` | In logical pixels, clipped to the output |

The NeferWL protocol XML is in `internal/adapters/wayland/imagecapture/`.

- Captures need Vulkan sync-file export; on devices without it, such as lavapipe, capture requests fail.
- Captures are opaque 8-bit sRGB. HDR content is tone-mapped on the GPU.
- The cursor is never included.
- Only one off-screen target (a window or hidden workspace) renders at a time. Other sessions wait for it, then fail. Off-screen targets are limited to 16384 px per side and 256 MiB per image.
- A session stops when its output, workspace or window goes away. A window that unmaps and maps again is a new window: capture it again.
- A frame whose target is briefly unavailable is retried for up to one second, so a short hiccup does not end a screencast.
- `neferwl_workspace_frame_v1` (from `get_workspace_frame`) reports a workspace's position and size, which ext-workspace does not carry.

## The indicator

Every capture is visible, and no client can turn that off. NeferWL draws a 2-logical-pixel red border (`#ff3b30`) around the captured output, region or on-screen workspace. A captured window always shows a small red square in the top-right corner of its output, plus its border while it is on screen. A hidden workspace shows only the square.

- A live session keeps the indicator on. A one-shot screenshot flashes it for one second.
- The indicator is never in the captured image.
- The indicator is on screen before or with the frame it captures. If it cannot be shown, a screencopy frame fails after 250 ms; an ext-image-copy-capture frame keeps retrying for up to 1 s.
- While it shows, the output is composed (no direct scanout), and capturing the displayed frame costs a second composition.

## Hiding a recorder's own HUD

`neferwl_capture_exclusion_manager_v1` keeps a recorder's HUD out of its own frames:

1. The recorder calls `get_exclusion` on its capture session and gets a token. It fails with `busy` if another exclusion is live.
2. The HUD attaches up to 4 layer surfaces (top or overlay, not yet mapped) with that token.
3. The HUD stays visible over fullscreen windows without taking keyboard focus. That session's frames leave it out; every other capture keeps it.

The HUD must run as the same user and be allowed to capture itself. The exclusion ends with the session, or when the recorder destroys it; its HUD surfaces are then closed.

## Who may capture

A client that may not capture gets its protocol's failure (`failed`, or `stopped` for a session). In order:

1. **Locked session:** nobody captures. See [Session locking](session-lock.md).
2. **Sandboxed client:** a client connected through `wp_security_context_v1` (Flatpak, for instance) never captures directly; it uses the portal. Sandboxed clients still see the capture globals, but their requests are refused. Other privileged protocols are not restricted. A sandbox engine must call `set_sandbox_engine` before `commit`, and a sandboxed client cannot create its own security context; both are protocol errors.
3. **Allowlist:** the executable must be listed.

## The allowlist

By default, these executables may capture:

```text
/usr/bin/grim
/usr/bin/nefercap
/usr/lib/xdg-desktop-portal-wlr
/usr/libexec/xdg-desktop-portal-wlr
```

To change it, create `/etc/neferwl/capture-allow`. It **replaces** the built-in list, so keep the entries you still need:

```sh
sudo install -Dm644 /usr/share/doc/neferwl/capture-allow.example /etc/neferwl/capture-allow
sudoedit /etc/neferwl/capture-allow
```

```text
# one absolute executable path per line
/usr/bin/grim
/usr/bin/nefercap
/usr/lib/xdg-desktop-portal-wlr
/opt/tools/bin/recorder
```

- List the real file, as `/proc/<pid>/exe` shows it: `/usr/bin/python3.12`, not a `python3` symlink.
- Paths must match exactly; there are no globs.
- A line with only `*` lets every client capture.
- Changes apply within a fraction of a second, or about 2 s after creating `/etc/neferwl`. They apply to the next request and to the next frame of running sessions; images already delivered cannot be taken back. If the file cannot be watched, NeferWL logs a warning and changes need a restart.
- If the file or `/etc/neferwl` is missing, the built-in list applies.
- If the file is invalid or unsafe, no client may capture. The log names the problem.

The path is fixed and not a config key: any program running as you could edit your config, but not a root-owned file.

### The file must belong to root

The file must be a regular file. It and every directory above it (`/`, `/etc`, `/etc/neferwl`) must be owned by root, not writable by group or others (sticky directories are accepted), and not symlinks. Otherwise no client may capture, even if `/` or `/etc` is the problem: the built-in list does not apply.

### How a client is identified

By the executable actually running, not its name or app ID. NeferWL takes a pidfd of the client (`SO_PEERPIDFD`, Linux 6.5+) and checks `/proc/<pid>/exe` at every screencopy frame, every new ext session and every HUD attach. A process that exits, or whose executable was deleted or replaced, is refused.

Frames of an open ext session reuse the executable found when the client was last checked. A program that `exec`s into another binary keeps its open sessions, and is judged by the new binary at its next request.

On kernels older than 6.5, no client may capture unless the list is `*`.

Each refused executable is logged once: `capture refused`, with the pid, path and reason.

### Limits

- **Binaries you own.** Listing a file under your home trusts every program that can overwrite it. Prefer root-owned paths.
- **Interpreters.** A script runs as its interpreter. Never list `python`, `bash` or `node`: every script would be allowed.
- **Same-user attacks.** The allowlist stops unknown programs. It does not stop a program running as you from driving a listed one (`LD_PRELOAD`, `ptrace`, or simply asking it). The indicator still shows.
- **What happens next.** A listed program can send its images anywhere.

## Portals

- **Screen sharing** in browsers and apps: install `xdg-desktop-portal-wlr`.
- **Screenshots and recordings:** install [nefercap](https://github.com/bnema/nefercap).

The packaged `neferwl-portals.conf` sends `ScreenCast` and `Screenshot` to the wlr portal and everything else to the GTK portal. You can share a whole output or a single window.

NeferWL has no window picker. The wlr portal starts one itself, such as slurp, wofi or bemenu: see `chooser_type` and `chooser_cmd` in `man xdg-desktop-portal-wlr`.

Any program running as you can ask the portal over D-Bus, and the wlr portal takes screenshots without a dialog. The indicator still flashes each time.
