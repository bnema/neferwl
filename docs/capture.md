# Screen capture

NeferWL serves `zwlr_screencopy_manager_v1` and `ext_image_copy_capture_manager_v1`, with `ext_output_image_capture_source_manager_v1` (an output) and `neferwl_image_capture_source_manager_v1` (a workspace, or a region of an output). `grim` uses either protocol; recorders use ext-image-copy-capture. The sources, buffers and limits are in [Headless mode](headless.md). The source XML of the NeferWL extensions is in `internal/adapters/wayland/imagecapture/`.

Three rules decide what a capture does. They apply to every protocol and every source.

## The indicator

Every capture is visible on screen, and no client can turn that off: the compositor draws a red border around the target of a capture while it lives and for one second after each captured frame. The indicator is in no captured image. Details are in [Headless mode](headless.md).

## Who may capture

One function of the Wayland adapter (`mayCapture`) decides, for wlr-screencopy frames, ext-image-copy-capture sessions and frames, and the attach of a layer surface to a capture exclusion. A client that may not capture gets the failure its protocol defines (`failed`, or `stopped` for a session), and nothing is captured or drawn. In order:

1. **A locked session.** While an `ext-session-lock` lock is held, no client may capture. See [Session locking](session-lock.md).
2. **A sandboxed client.** A client that connects through a `wp_security_context_v1` socket (Flatpak, for instance) never captures, whatever the allowlist says: it asks the desktop portal, which runs outside the sandbox. NeferWL serves `wp_security_context_manager_v1`. It accepts connections on the socket a sandbox engine hands it until the engine closes `close_fd` or NeferWL stops, and a sandboxed client cannot create a security context itself (the `nested` protocol error). The engine must call `set_sandbox_engine` before `commit`; a context committed without it is an `invalid_metadata` protocol error, so every sandboxed client is tagged with its engine. NeferWL cannot hide globals from one client, so a sandboxed client still sees the capture globals; its requests are refused. Only capture is restricted for sandboxed clients: other privileged protocols, such as virtual keyboard, output management or data control, are not.
3. **The executable allowlist**, below.

## The allowlist

NeferWL ships a built-in allowlist. These executables may capture:

```text
/usr/bin/grim
/usr/bin/nefercap
/usr/lib/xdg-desktop-portal-wlr
/usr/libexec/xdg-desktop-portal-wlr
```

`/etc/neferwl/capture-allow` replaces the built-in list entirely when it exists. The path is fixed. It is not a config key, because a program running as your user could edit the config and give itself access; the file belongs to root. The package does not install the file.

To allow a tool installed elsewhere, copy the example to the fixed path as root and add the path. Keep the built-in entries you still need, because the file replaces them:

```sh
sudo install -Dm644 /usr/share/doc/neferwl/capture-allow.example /etc/neferwl/capture-allow
sudoedit /etc/neferwl/capture-allow
```

Each line is the resolved executable path, the one `/proc/<pid>/exe` shows. Symlinks in the list are not followed: list the real file (`/usr/bin/python3.12`, not a `/usr/bin/python3` link to it), because the kernel reports the target of a link, never the link.

```text
# one absolute executable path per line
/usr/bin/grim
/usr/bin/nefercap
/usr/lib/xdg-desktop-portal-wlr
/usr/libexec/xdg-desktop-portal-wlr
/opt/tools/bin/recorder
```

- Blank lines and lines starting with `#` are ignored.
- A line with only `*` disables the check: every client may capture.
- A missing file means the built-in list. NeferWL logs one info line (`capture allowlist file missing, using the built-in list`) with the path. Deleting the file goes back to the built-in list.
- An unreadable or invalid file (a relative path, or anything that is not a path) lets no client capture. NeferWL logs an error naming the line and keeps running; fix the file and it recovers.
- An untrustworthy file lets no client capture either, and is logged as an error naming the problem (see below).
- Comparison is exact, on the cleaned path. There are no globs or directories.

NeferWL reads the file at startup and again whenever it changes, within a fraction of a second. It watches the directory, so replacing the file atomically works. If `/etc/neferwl` does not exist yet it checks again every two seconds, so creating the directory and the file applies within about two seconds. A change applies to the next capture request of every client; sessions and frames already captured are not revoked. If the watch cannot be set up, NeferWL logs a warning and the file stays as loaded: restart to apply changes.

### The file must be root's

The allowlist is only as good as the ability to write it. NeferWL therefore checks, at every load:

- the file is opened without following a symlink and without blocking, and the open descriptor must be a regular file owned by root (uid 0) and writable by nobody but its owner (no group or other write bit);
- every directory from `/` down to the file's (`/etc`, `/etc/neferwl`) must be a real directory, not a symlink, owned by root and not group- or other-writable (a sticky directory is accepted).

Anything else (a symlink, a FIFO, a file or directory owned by your user or writable by others) lets no client capture, and NeferWL logs an error that names the file or directory. A missing file, or a missing `/etc/neferwl` under a trustworthy `/etc`, gives the built-in list. If `/etc` or its ancestors fail the checks, the result is the closed policy, not the built-in list.

### What identifies a client

The executable actually running behind the connection, not its name, app-id or `argv[0]`. The first time a client asks to capture, NeferWL takes a pidfd of the peer (`SO_PEERPIDFD`, Linux 6.5 and later) and keeps it until the client disconnects. It pins the process, so its pid cannot be reused. At every wlr-screencopy copy, every ext-image-copy-capture session creation and every exclusion attach, NeferWL reads the pid from the pidfd, reads `/proc/<pid>/exe`, then checks that the process is still alive. The verdict is never cached, only the pidfd: a program that `exec`s another binary after connecting is judged by the new one at its next request. The frames of an ext session do not read `/proc` again: they use the executable resolved at the client's latest check (the creation of a session, or a later wlr-screencopy request or session on the same connection). The current allowlist, the lock and the sandbox tag are still applied to each of them. A process that exited is refused, and so is an executable that was deleted or replaced on disk (`(deleted)`), or one that cannot be determined.

There is no fallback for a kernel without `SO_PEERPIDFD`: the pid from `SO_PEERCRED` could be reused between the read and the check, so it identifies nobody. With a list (built-in or from the file), no client may capture on such a kernel, and NeferWL logs one warning the first time a client asks. Use a kernel 6.5 or later, or `*` to disable the check.

A refused client is logged once per distinct executable, at info level: `capture refused` with the pid, the executable path and the reason. No token or request content is logged.

### Limits

- **Binaries you own.** An executable your user can write to, such as one under your home directory, can be replaced by any program of yours. Listing it trusts every program that can write there. Prefer root-owned paths.
- **Interpreters.** A script runs as its interpreter: the executable behind the connection is `/usr/bin/python3`, not the script. Never list `python`, `bash`, `node` or any other interpreter, because every script they run would be allowed to capture. A program in such a language that needs capture has to be started through a compiled launcher that you list.
- **Threat model.** The allowlist stops unknown programs from capturing directly. It does not stop an attacker running as your user who drives or injects into a listed program: handing the connection's file descriptor to a child after `exec`, `LD_PRELOAD` into a listed program, `ptrace`, or simply asking it to capture. Such a program is then the one the kernel reports, and it is allowed. The indicator still shows while it captures.
- The allowlist limits who may ask, not what they do with the image. Whatever a listed program receives, it can send anywhere.

## Portals

For screen sharing in browsers and other applications, install `xdg-desktop-portal-wlr`; for screenshots and recordings, install [nefercap](https://github.com/bnema/nefercap). The packaged `neferwl-portals.conf` sends `ScreenCast` and `Screenshot` to the wlr portal and everything else to the GTK portal. The wlr portal is in the built-in list as `/usr/lib/xdg-desktop-portal-wlr` and `/usr/libexec/xdg-desktop-portal-wlr`; if it lives elsewhere, add its path in `/etc/neferwl/capture-allow`. What to share is picked with a chooser that the portal starts itself, an external tool such as slurp, wofi or bemenu (see `chooser_type` and `chooser_cmd` in the `xdg-desktop-portal-wlr` man page). NeferWL has no chooser of its own, so install one. Sandboxed applications use the same portal: they never capture directly.

A listed program captures on behalf of whoever asks it. Any program running as your user can ask the portal over D-Bus, and the wlr portal takes screenshots without a dialog. The indicator still flashes for each of them.
