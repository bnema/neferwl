#!/bin/sh
# Regenerates imagecapture.go from neferwl-image-capture-v1.xml with the wlbgen
# of the go-wayland-bindings module in use (offline: go.work or the module cache).
set -eu
cd "$(dirname "$0")"
go run github.com/bnema/go-wayland-bindings/cmd/wlbgen -side server \
  -package imagecapture -out imagecapture.go \
  neferwl-image-capture-v1.xml
