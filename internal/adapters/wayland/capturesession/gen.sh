#!/bin/sh
# Regenerates capturesession.go from neferwl-capture-v1.xml with the wlgen of
# the purego-libwayland module in use (offline: go.work or the module cache).
set -eu
cd "$(dirname "$0")"
mod=$(go list -m -f '{{.Dir}}' github.com/bnema/purego-libwayland)
go run github.com/bnema/purego-libwayland/cmd/wlgen -package capturesession -out capturesession.go \
  -import wayland=github.com/bnema/purego-libwayland/protocol/wayland -import-xml wayland="$mod/protocols/wayland.xml" \
  neferwl-capture-v1.xml
