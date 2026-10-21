# syntax=docker/dockerfile:1
FROM archlinux:latest
RUN --mount=type=cache,target=/var/cache/pacman/pkg,sharing=locked \
    pacman -Sy --noconfirm archlinux-keyring \
    && pacman -Syu --noconfirm --needed \
      firefox foot btop fastfetch eza mousepad \
      ttf-jetbrains-mono-nerd noto-fonts noto-fonts-emoji dbus mesa vulkan-swrast
# Arch's btop carries file capabilities that rootless Docker cannot grant.
RUN install -m 0755 /usr/bin/btop /usr/local/bin/btop
RUN useradd -m -u 1000 demo \
    && mkdir -p /home/demo/firefox /home/demo/.config/btop /run/wl \
    && chmod 0700 /run/wl
COPY firefox/user.js /home/demo/firefox/user.js
COPY firefox/userChrome.css /home/demo/firefox/chrome/userChrome.css
COPY foot.ini /home/demo/.config/foot/foot.ini
COPY btop.conf /home/demo/.config/btop/btop.conf
COPY btop-vesper.theme /home/demo/.config/btop/themes/vesper.theme
COPY site /home/demo/site
COPY config /home/demo/config
RUN chown -R demo:demo /home/demo /run/wl
ENV HOME=/home/demo XDG_RUNTIME_DIR=/run/wl WAYLAND_DISPLAY=wayland-0 \
    GTK_THEME=Adwaita:dark ADW_DEBUG_COLOR_SCHEME=prefer-dark \
    MOZ_ENABLE_WAYLAND=1 LIBGL_ALWAYS_SOFTWARE=1 COLORTERM=truecolor
WORKDIR /home/demo
# The recorder grants demo access to only the disposable headless socket.
CMD ["sleep", "infinity"]
