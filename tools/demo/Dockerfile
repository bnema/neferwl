# syntax=docker/dockerfile:1
FROM archlinux:latest
RUN --mount=type=cache,target=/var/cache/pacman/pkg,sharing=locked \
    pacman -Sy --noconfirm archlinux-keyring \
    && pacman -Syu --noconfirm --needed \
      firefox foot btop fastfetch eza mousepad \
      ttf-jetbrains-mono-nerd noto-fonts noto-fonts-emoji dbus mesa vulkan-radeon \
      tesseract tesseract-data-eng at-spi2-core python
# Arch's btop carries file capabilities that rootless Docker cannot grant.
RUN install -m 0755 /usr/bin/btop /usr/local/bin/btop
# Mousepad warns about running as root, so it runs as demo (uid 1000).
RUN useradd -m -u 1000 demo && mkdir -p /root/firefox /root/.config/btop /run/wl && chmod 0755 /run/wl
COPY firefox/user.js /root/firefox/user.js
COPY firefox/userChrome.css /root/firefox/chrome/userChrome.css
COPY foot.ini /root/.config/foot/foot.ini
COPY btop.conf /root/.config/btop/btop.conf
COPY btop-vesper.theme /root/.config/btop/themes/vesper.theme
COPY demo-entry demo-hint-label /usr/local/bin/
# One config for both users: the compositor (root) reads it, Mousepad (demo) edits it.
COPY config /home/demo/config
COPY mousepad.keyfile /home/demo/.config/glib-2.0/settings/keyfile
RUN chown -R demo:demo /home/demo
ENV HOME=/root XDG_RUNTIME_DIR=/run/wl WAYLAND_DISPLAY=wayland-0 \
    GTK_THEME=Adwaita:dark ADW_DEBUG_COLOR_SCHEME=prefer-dark \
    DBUS_SESSION_BUS_ADDRESS=unix:path=/run/wl/bus \
    MOZ_ENABLE_WAYLAND=1 LIBGL_ALWAYS_SOFTWARE=1 COLORTERM=truecolor
# LIBGL_ALWAYS_SOFTWARE applies to the apps' OpenGL, not to the compositor (Vulkan).
WORKDIR /root
# Everything runs as the container's root, which rootless Docker maps to the
# host user, so the bind-mounted work directory stays owned by the host user.
# The compositor (bind-mounted at /usr/local/bin/neferwl) and the apps share one
# session bus, which AT-SPI needs. The compositor renders on the host GPU through
# /dev/dri: vulkan-radeon is its Mesa driver here (swap in vulkan-intel for an
# Intel GPU). Software Vulkan (lvp) cannot export the sync files hint scans need.
ENTRYPOINT ["/usr/local/bin/demo-entry"]
