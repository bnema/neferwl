<h1 align="center">NeferWL</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPLv3-blue?style=flat-square" alt="License: AGPLv3"></a>
  <a href="https://github.com/bnema/neferwl"><img src="https://img.shields.io/badge/platform-Linux-blue?style=flat-square" alt="Platform: Linux"></a>
  <a href="https://github.com/bnema/neferwl/commits/main"><img src="https://img.shields.io/github/last-commit/bnema/neferwl/main?style=flat-square&logo=github" alt="Last commit"></a>
</p>

<p align="center">A Wayland compositor that spends its frames on your apps, not on itself.</p>

https://github.com/user-attachments/assets/1ff42ce5-47b3-4cf8-a3a7-5fc5abde4af1

<p align="center"><sub>Stash, overview and animations, recorded on the headless backend at 1080p.</sub></p>

> [!WARNING]
> **Early alpha, but stable enough for daily use.** Developed and tested mostly on AMD CPUs and GPUs. NVIDIA support is incomplete and untested (contribs welcome). Expect bugs and breaking config changes.

## Why NeferWL

- **Performance:** allocation-free hot paths, no wasted CPU passes and no cgo; about 40–80 MB of RAM with two 4K monitors. On 4K HDR video, GC takes under 9% of a process using 13% of a core; see [Architecture](docs/dev/architecture.md#keeping-the-garbage-collector-out-of-the-way) and [Performance](docs/performance.md).
- **Games:** direct scanout, VRR, tearing, explicit sync and [HDR](docs/config.md#hdr) give games the whole GPU instead of spending it on composition; see [Performance](docs/performance.md).
- **Overview:** `cmd+o` or a four-finger swipe up shows live workspace previews. Select a window with the keyboard, touchpad or mouse; see [Overview](docs/config.md#overview).
- **Stash:** `cmd+s` toggles a floating strip of windows set aside for the current workspace; `cmd+shift+s` moves a window into or out of it. See [Stash](docs/config.md#stash).
- **Hint mode:** click what is on screen by typing its label, without reaching for the mouse; targets come from the `nefer-hints` detector. Enable experimental `hint.enable` and add a bind; see [Hint mode](docs/config.md#hint-mode).
- **Animations:** short, cheap and off by default. Opt in with experimental `animations = on`; see [Configuration](docs/config.md#keys).

Written in Go, without cgo. Each piece of state has one owner goroutine, connected to the others by channels; hot paths reuse memory and client buffers stay off the Go heap. See [Architecture](docs/dev/architecture.md) for the concurrency model, the GC strategy and the reusable Go libraries.

Small on purpose, and staying small: no blur, shadows or themes. No built-in bar or wallpaper either: bring your own, any layer-shell client works.

## Install

### Arch Linux

```sh
paru -S neferwl-bin   # latest release, pre-built for x86_64 and aarch64
paru -S neferwl-git   # latest main, built from source
```

Or install from a checkout with Go 1.27:

```sh
make install   # build a package from HEAD and install it with pacman
```

`make pkg` builds the package without installing it.

### Ubuntu 24.04 and later

Download the `.deb` for your architecture (`amd64` or `arm64`) and `checksums.txt` from [Releases](https://git.bnema.dev/neferwl/releases), then install it with apt, which pulls the runtime dependencies:

```sh
sha256sum -c --ignore-missing checksums.txt
sudo apt install ./neferwl_<version>_amd64.deb
```

```sh
sudo apt install foot fuzzel xdg-desktop-portal-gtk xdg-desktop-portal-gnome
```

X11 apps need [xwayland-satellite](https://github.com/Supreeeme/xwayland-satellite) 0.7 or later, which you may have to build. The package is tested on Ubuntu 24.04 and 26.04; Debian with the same library packages should work but is untested.

The packages provide:

- `neferwl` with CAP_SYS_NICE for real-time scheduling.
- A **NeferWL** session for display managers such as Ly, GDM and SDDM.
- `neferwl-session`, which starts `neferwl.service` and manages `graphical-session.target` so desktop services start and stop with the session.

Running `neferwl --session` directly also exports the display environment to D-Bus and systemd user services. See [Desktop integration](docs/desktop.md) for session setup.

Copy `/usr/share/doc/neferwl/config.example` to `~/.config/neferwl/config` to start from the example config.

### Other distributions

Download a `linux_amd64` or `linux_arm64` archive from [Releases](https://git.bnema.dev/neferwl/releases) and verify it against `checksums.txt`. To build locally, run `make bin`; the binary is `bin/neferwl`.

## Configuration

The default file is `$XDG_CONFIG_HOME/neferwl/config`, or `~/.config/neferwl/config`.

```text
keyboard.layout = fr
terminal.auto-open = first
startup = waybar

output.DP-1 = 3840x2160@144
output.DP-1.scale = 1.5

layout.max-columns = 3

# Start and place an editor and terminal when this workspace is opened.
workspace.dev.monitor = DP-1
workspace.dev.column.1 = 67%, code --new-window
workspace.dev.column.2 = 33%, foot
bind.cmd+d = workspace dev
```

- Use one `key = value` per line; `#` starts a comment.
- Missing keys use defaults. Invalid lines warn and keep that key's default; other lines still apply.
- Save to reload. The log lists changed keys.
- Use `neferwl --config <path>` to select a file, or `neferwl validate-config [path]` to check it without starting the compositor.

### Common settings

| Key | Default | Meaning |
| --- | --- | --- |
| `keyboard.layout` | system | xkb layout, e.g. `fr` |
| `keyboard.cmd` | `super` | Modifier used as `cmd` |
| `terminal` | `$TERMINAL`, then `foot` | Terminal command |
| `terminal.auto-open` | `first` | Open on initial numbered workspace; also `all` or `off` |
| `startup` | none | Session-start commands, separated by `;` |
| `layout.max-columns` | `2` | Columns sharing the screen |
| `layout.overflow` | `cascade` | Overflow layout: `cascade`, `scroll`, `fixed` |
| `layout.gaps` | `0` | Space between windows |
| `output.<name>` | `preferred` | Mode: `WxH@Hz`, `preferred` or `off` |
| `output.<name>.scale` | `1` | Fractional scale, 1 to 4 |
| `output.<name>.hdr` | `off` | HDR on capable displays |
| `touchpad.natural-scroll` | `off` | Content follows fingers |
| `stash.width` | `80` | Selected stash window width, percent |
| `hint.enable` | `off` | Experimental keyboard clicking; restart required |
| `animations` | `off` | Experimental layout and swipe animations |

### Default key bindings

`cmd` means Super by default. Hint mode has no default binding.

| Keys | Action |
| --- | --- |
| `cmd+return` | Open terminal |
| `ctrl+cmd+space` | Launch fuzzel |
| `cmd+q` | Close window |
| `cmd+arrows` / `cmd+h/j/k/l` | Focus columns and windows |
| `cmd+shift+arrows` | Move columns and windows |
| `cmd+o` | Toggle overview |
| `cmd+s` | Show/hide stash |
| `cmd+shift+s` | Move window into/out of stash |
| `cmd+shift+f` | Toggle fullscreen |
| `cmd+shift+space` | Toggle floating |
| `cmd+1`…`9` (physical digit keys) | Focus numbered workspace |
| `cmd+pageup/pagedown` | Previous/next workspace |

See [Configuration](docs/config.md) for all keys, actions, bindings and automatic-terminal behavior, or [examples/config](examples/config) for commented defaults.

## Features

### Display and games

- Eligible fullscreen buffers use direct scanout; VRR and tearing are on by default for supported fullscreen use (`render.vrr`, `render.tearing`). Tearing requires direct scanout and a client request.
- Explicit sync carries client fences to the GPU and KMS, enabled by default with no flag. HDR10 on capable displays includes configurable SDR brightness.
- Separate workspaces and fractional scale per output; workspaces move when a monitor is unplugged and return when it reconnects.
- Hidden apps get frame callbacks once per second and the suspended state, whether on another workspace, in a hidden stash, off screen or under fullscreen. Captured windows keep their output's refresh rate.
- Steam and other X11 clients run through xwayland-satellite; Wine can run natively on Wayland.

### Windows and workspaces

- [Cascade tiling](docs/config.md#cascade) is the default; horizontal scrolling (as in PaperWM and niri) and fixed spiral layouts are available per output or workspace.
- Numbered workspaces appear as needed; named workspaces can start commands and place windows in predefined columns.
- Overview and stash keep workspaces and set-aside windows close at hand, with keyboard, pointer and touchpad navigation.

### Desktop integration

Bring your own layer-shell UI, such as Noctalia or Waybar. For capture and idle management, see [nefercap](https://github.com/bnema/nefercap) and [neferafk](https://github.com/bnema/neferafk).

Configuration is a flat `key = value` file that reloads on save. A JSON state file exposes outputs, workspaces and windows to scripts; see [Desktop integration](docs/desktop.md).

## Hardware support

NeferWL requires Linux, atomic KMS and a Vulkan 1.3 device that can export images as dmabufs. It reports an error at startup if these requirements are not met.

| GPU | Status |
| --- | --- |
| AMD | Developed and tested on radv |
| NVIDIA | Untested; explicit sync is implemented, but support is incomplete |
| Intel | Untested |

Reports from NVIDIA and Intel users are welcome.

## Documentation

- [Configuration](docs/config.md): keys, actions, bindings, layouts, overview, stash, hint mode and HDR.
- [Desktop integration](docs/desktop.md): sessions, clipboard, input methods, X11 apps, session locking and scripting.
- [Screen capture](docs/capture.md): protocols, indicator, executable allowlist and portals.
- [Performance](docs/performance.md): VRR and real-time priority.
- [Developer documentation](docs/dev/index.md): architecture, headless testing, profiling and frame stats.

## Contributing

Issues are welcome, including NVIDIA and Intel reports. Please open an issue before a large pull request; small fixes are fine directly.

Before submitting, run `make check`, `make race`, `make mocks-check` and `staticcheck ./...`. See [AGENTS.md](AGENTS.md) for repository rules.

## License

[GNU Affero General Public License v3.0](LICENSE).
