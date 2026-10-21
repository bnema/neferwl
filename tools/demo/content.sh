#!/usr/bin/env bash
# Static, colorful terminal content for the recording (no personal shell config).
# These listings are displayed, not parsed; truncation is intentional.
# shellcheck disable=SC2012
set -euo pipefail
clear
case ${1:-} in
    system)
        fastfetch --logo small --structure OS:Kernel:WM:Terminal:Memory:Colors
        ls -la --color=always /etc | head -24 || true
        ;;
    files)
        ls -l --color=always /usr/share | head -34 || true
        ;;
    stash)
        printf '\033[1;36m  NEFERWL / YOUR STASH\033[0m\n\n'
        for ((i = 0; i < 6; i++)); do
            printf '\033[1;34m  SET ASIDE\033[0m   a window with Super+Shift+s\n'
            printf '\033[1;32m  BRING BACK\033[0m  a window with Super+s\n'
            printf '\033[1;35m  STAY FOCUSED\033[0m keep your workspace clear\n\n'
        done
        ;;
esac
