# Screen capture

NeferWL supports `zwlr_screencopy_manager_v1` (used by `grim`) and `ext_image_copy_capture_manager_v1` (used by recorders).

- A red indicator shows on screen while something is captured.
- Only allowed executables may capture. See [the allowlist](#the-allowlist).
- Sandboxed apps (Flatpak) must go through the [portal](#portals).

## Sources

With ext-image-copy-capture, a client can capture an output, a window, a workspace (even off screen, without bars) or a region of an output. The workspace and region sources use the NeferWL protocol in `internal/adapters/wayland/imagecapture/`.

- Captures are 8-bit sRGB. HDR content is tone-mapped.
- The cursor is never included.
- GPUs without Vulkan sync-file export (such as lavapipe) cannot capture.
- `neferwl_capture_exclusion_manager_v1` lets a recorder keep its own HUD out of its frames.

## The indicator

A 2-pixel red border surrounds the captured output, region or workspace. A captured window adds a small red square in the top-right corner of its output. No client can turn it off, and it never appears in the captured image.

A screenshot flashes the indicator for one second; a recording keeps it on.

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

- List the real file, as `/proc/<pid>/exe` shows it: `/usr/bin/python3.12`, not a `python3` symlink. No globs.
- A line with only `*` lets every client capture.
- Changes apply without a restart.
- The file and every directory above it must be owned by root, not writable by group or others, and not symlinks.
- If the file is invalid or unsafe, nobody may capture. The log says why.
- Refused clients are logged as `capture refused`, with the pid and path.
- Client checks need Linux 6.5 or later. On older kernels, nobody may capture unless the list is `*`.

Never list an interpreter (`python`, `bash`, `node`): every script it runs could capture. Avoid listing files you own: any program running as you could replace them.

The allowlist stops unknown programs, not a program running as you that drives a listed one. The indicator still shows.

## Portals

- **Screen sharing** in browsers and apps: install `xdg-desktop-portal-wlr`.
- **Screenshots and recordings:** install [nefercap](https://github.com/bnema/nefercap).

The packaged `neferwl-portals.conf` routes `ScreenCast` and `Screenshot` to the wlr portal. NeferWL has no window picker: set `chooser_type` and `chooser_cmd` in the wlr portal config to use slurp, wofi or bemenu (see `man xdg-desktop-portal-wlr`).

The wlr portal takes screenshots without asking, for any program running as you. The indicator still flashes.
