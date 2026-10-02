<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPLv3-blue?style=flat-square" alt="License: GPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://badgen.net/github/last-commit/bnema/neferwl/main?icon=github" alt="Last commit"></a>
  <a href="https://github.com/bnema/neferwl/stargazers"><img src="https://badgen.net/github/stars/bnema/neferwl?icon=github" alt="GitHub stars"></a>
</p>

<p align="center">A Wayland compositor that spends its frames on your apps, not on itself.</p>

> [!WARNING]
> **Early alpha.** NeferWL is developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested; expect bugs and breaking config changes.

---

## Why NeferWL

I wanted a compositor I would never have to think about. It takes the newest Wayland protocols and kernel features as they land, gives games the whole GPU, and otherwise stays out of sight.

It is small on purpose, and it will stay small. No blur, no shadows, no rounded corners, no themes, no built-in bar or wallpaper. The only motion is a touchpad swipe that follows your fingers, and a brief pulse on the window you just focused, which you can turn off. Anything else that costs a frame is left out. About 40–80 MB of RAM with two 4K monitors, and almost no CPU while the screen does not change.

The config fits in your head: one `key = value` per line, every key has a default, and an empty file is a valid config. See [Configuration](#configuration).

## Features

- **Games.** Fullscreen is exclusive: nothing is drawn above a fullscreen window but a locker, so it is scanned out directly, with tearing, VRR and explicit sync when the client asks for them. Wine runs natively on Wayland; Steam and other X11 clients run through xwayland-satellite. See [the performance path](#the-performance-path).
- **HDR.** HDR10 output on capable displays. HDR clients (games, browsers, video players) are shown at full range; SDR content is shown at a configured brightness.
- **Column tiling.** Columns scroll to the right, as in PaperWM and Niri, or stay on screen and split, as in Sway. Per output or per workspace.
- **Workspaces.** Numbered workspaces are created and removed as needed. Named workspaces declare their columns (width and command); NeferWL starts the commands and places each window.
- **Multi-monitor.** Each output has its own workspaces. When an output is unplugged its workspaces move to another one, and return when it is plugged back. Fractional scale per output.
- **External clients.** No built-in bar, launcher or notifications. Waybar, fuzzel, mako, nefercap, cliphist, swayidle, wlr-randr and input methods such as fcitx5 work through their standard protocols. See [Desktop integration](docs/desktop.md).
- **Simple config.** One flat `key = value` file, reloaded on save without closing windows. A bad line only warns and keeps its default, and `neferwl validate-config` checks a file before you use it. A JSON state file describes outputs, workspaces and windows for scripts.

Config example:

```text
keyboard.layout = fr
terminal.auto-open = first
startup = waybar

output.DP-1 = 3840x2160@144
output.DP-1.scale = 1.5

layout.max-columns = 3

# A bind-only dev workspace: editor and terminal, started and placed for you.
workspace.dev.monitor = DP-1
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 33%, foot
bind.cmd+d = workspace dev
```

The terminal command comes from `terminal`, then `$TERMINAL`, then `foot`. `terminal.auto-open = first` opens it once on the initial numbered workspace; `all` opens it on each visible empty workspace without slots, and `off` disables automatic opening. `--no-terminal` overrides automatic opening for one run; `spawn-terminal` still works. Launched programs inherit `$SHELL`, `$EDITOR`, and `$VISUAL`.

## The performance path

- `zwp_linux_dmabuf_v1` v4: client buffers are sampled in place. Vulkan composes into exported scanout images and the frame fence goes to KMS as `IN_FENCE_FD`. No CPU copy, no CPU wait.
- Direct scanout of a fullscreen window's buffer; dmabuf feedback gives it a scanout-ready format first. A lone opaque window can use an overlay plane.
- `wp_tearing_control_v1` and VRR while a fullscreen window covers the output.
- `wp_linux_drm_syncobj_v1`: client fences go to KMS. NVIDIA drivers need it.
- `wp_presentation`, `wp_fifo_v1`, `wp_commit_timing_v1` for frame pacing.
- `wp_fractional_scale_v1` and `wp_viewporter`: clients render at the output scale, nothing is rescaled.
- Damage tracking, occlusion of surfaces behind opaque ones, no work on idle outputs. Frame, cursor, overlay and VRR in one atomic commit.
- Input is read on its own thread and never waits on rendering; the cursor has a hardware plane. With CAP_SYS_NICE, input and output threads request real-time scheduling. Relative pointer, pointer constraints and keyboard shortcuts inhibit for games.
- `wp_drm_lease_v1` leases headset connectors to SteamVR and Monado.

## Hardware support

NeferWL runs on Linux. It needs a GPU and kernel driver with atomic KMS, and a Vulkan 1.3 device that can export images as dmabufs. Without them, it stops at startup with an error.

- **AMD:** developed and tested on radv.
- **NVIDIA:** untested. Explicit sync, which NVIDIA drivers need, is implemented. Reports from NVIDIA users are welcome.
- **Intel:** untested.

## Install (Arch Linux)

From the AUR:

```sh
paru -S neferwl-bin   # latest release, pre-built for x86_64 and aarch64
paru -S neferwl-git   # latest main, built from source
```

From a checkout (needs Go 1.27):

```sh
make install   # builds dist/neferwl-*.pkg.tar.zst from HEAD, then installs it with pacman
```

`make pkg` only builds the package. The package installs `neferwl` with CAP_SYS_NICE (real-time scheduling, see [performance](docs/performance.md)) and a **NeferWL** session for display managers such as Ly, GDM and SDDM. The session runs `neferwl-session`, which starts `neferwl.service` under systemd. This starts `graphical-session.target` and services bound to it (bars, notification daemons, portals, and XDG autostart) with NeferWL and stops them with it. Running `neferwl --session` alone still exports the display environment to D-Bus and systemd user services.

On other distributions, download a `linux_amd64` or `linux_arm64` archive from [Releases](https://github.com/bnema/neferwl/releases) and check it against `checksums.txt`, or run `make bin` to build `bin/neferwl`.

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`. It runs the CI checks, then GoReleaser builds the archives, `checksums.txt`, a provenance attestation and the GitHub release. Last, `packaging/aur/publish.sh` builds `neferwl-bin` and `neferwl-git` with makepkg, checks every source checksum for x86_64 and aarch64, and pushes them to the AUR. Pre-release tags (`v0.2.0-rc1`) get a GitHub release and are kept off the AUR.

The workflow needs the `AUR_SSH_PRIVATE_KEY`, `AUR_USERNAME` and `AUR_EMAIL` repository secrets.

## Configuration

NeferWL reads `$XDG_CONFIG_HOME/neferwl/config`, or `~/.config/neferwl/config`. `neferwl --config <path>` selects another file, and `neferwl validate-config [path]` checks a file without starting the compositor.

The file has one `key = value` per line, and `#` starts a comment. A missing file or key means the default. An invalid line logs a warning and keeps that key's default; the rest of the file still applies. Changes apply when you save, and each reload logs the keys that changed.

[docs/config.md](docs/config.md) lists every key, action and default bind. `examples/config` is a commented copy of the defaults.

## Documentation

- [Configuration](docs/config.md): every key, action and default bind, and HDR.
- [Desktop integration](docs/desktop.md): running commands, clipboard, input methods, X11 apps, idle and screen off, bars, and the state file for scripts.
- [Session locking](docs/session-lock.md): external lockers, output confirmation, owner recovery and security limits.
- [Screen capture](docs/capture.md): capture protocols, the indicator, the executable allowlist, sandboxed clients and portals.
- [Headless mode](docs/headless.md): run without a screen, take screenshots, play input scripts and test input methods.
- [Performance](docs/performance.md): how to profile, the allocation guards, and reference numbers.

## But why Go?

Most Wayland compositors are written in C (wlroots, Sway, Mutter), C++ (KWin, Hyprland, gamescope) or Rust (niri, COSMIC). The usual objection to Go is the garbage collector. It is a fair one, so here is how NeferWL deals with it.

- **The hot path does not allocate.** Frame callbacks, presentation reports, DRM flips, Vulkan submissions and surface updates reuse memory owned by the goroutine that handles them. `make check` runs allocation guards that fail the build when one of these paths starts allocating.
- **The GC has almost nothing to collect.** Client buffers live in shared and GPU memory, not on the Go heap, so the live heap is a few megabytes. Playing a 4K HDR video with 65 tiled subsurfaces committing every frame, GC takes under 9 % of a process that itself takes 13 % of a core. See [Performance](docs/performance.md).
- **No cgo.** NeferWL builds with `CGO_ENABLED=0`. libwayland, Vulkan and libinput are loaded at runtime through [purego](https://github.com/ebitengine/purego) with fixed-arity calls: no reflection, no allocation at the boundary.
- **The concurrency model fits.** Input, each output and each client own their own state in their own goroutine, and talk over channels. There is no mutex on window state, and the race detector checks every test.
- **It is a pleasure to work on.** A full build takes seconds, and tests, race detection, profiling and formatting come with the language.

I also like bringing more tools to the Go ecosystem. NeferWL grew its own libraries along the way, and other Go projects can use them:

- [purego-libwayland](https://github.com/bnema/purego-libwayland): libwayland-server without cgo.
- [purego-vulkan](https://github.com/bnema/purego-vulkan): Vulkan without cgo, generated from `vk.xml`.
- [go-wayland-bindings](https://github.com/bnema/go-wayland-bindings): Wayland protocol bindings, generated from the upstream XML.
- [wlturbo](https://github.com/bnema/wlturbo): a fast Wayland client.
- [neferclient](https://github.com/bnema/neferclient): a client toolkit on top of them: connection, outputs, surface roles, seat and dmabuf presentation.

## Developing the bindings

NeferWL uses tagged releases of [purego-libwayland](https://github.com/bnema/purego-libwayland) and [purego-vulkan](https://github.com/bnema/purego-vulkan). To change a binding and NeferWL together, clone the binding next to NeferWL and use a Go workspace, which is not committed:

```sh
go work init . ../purego-libwayland ../purego-vulkan
```

`make pkg` ignores the workspace and builds the versions in `go.mod`. Tag the binding change and update `go.mod` before packaging.

## Contributing

Contributions are welcome. Open an issue first, for a bug fix or a feature, and wait until it is accepted before you send a pull request. Pull requests without an accepted issue are closed.

Before you open a pull request, `make check`, `make race`, `make mocks-check` and `staticcheck ./...` must pass. Repository rules are in [AGENTS.md](AGENTS.md).

## License

NeferWL is licensed under the [GNU General Public License v3.0](LICENSE).
