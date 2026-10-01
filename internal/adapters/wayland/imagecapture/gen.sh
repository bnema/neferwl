#!/bin/sh
# Regenerates imagecapture.go from neferwl-image-capture-v1.xml with the wlgen
# of the purego-libwayland module in use (offline: go.work or the module cache).
set -eu
cd "$(dirname "$0")"
mod=$(go list -m -f '{{.Dir}}' github.com/bnema/purego-libwayland)
go run github.com/bnema/purego-libwayland/cmd/wlgen -package imagecapture -out imagecapture.go \
  -import wayland=github.com/bnema/purego-libwayland/protocol/wayland -import-xml wayland="$mod/protocols/wayland.xml" \
  -import extworkspace=github.com/bnema/purego-libwayland/protocol/extworkspace -import-xml extworkspace="$mod/protocols/ext-workspace-v1.xml" \
  -import extimagecapturesource=github.com/bnema/purego-libwayland/protocol/extimagecapturesource -import-xml extimagecapturesource="$mod/protocols/ext-image-capture-source-v1.xml" \
  -import extimagecopycapture=github.com/bnema/purego-libwayland/protocol/extimagecopycapture -import-xml extimagecopycapture="$mod/protocols/ext-image-copy-capture-v1.xml" \
  neferwl-image-capture-v1.xml
