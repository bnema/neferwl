#!/bin/sh
# Installs a neferwl .deb in a clean Ubuntu container and checks that it runs.
# Usage: packaging/deb/smoke-test.sh <ubuntu-version> <path/to/neferwl.deb>
# Needs CAP_SYS_NICE in the container: postinst sets it as a file capability.
set -eu

version=$1
deb=$(realpath "$2")

docker run --rm --cap-add SYS_NICE -v "$deb:/tmp/neferwl.deb:ro" "ubuntu:$version" sh -euc '
	# Ubuntu images skip /usr/share/doc; the example config lives there.
	rm -f /etc/dpkg/dpkg.cfg.d/excludes
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq /tmp/neferwl.deb >/dev/null
	getcap /usr/bin/neferwl | grep -q cap_sys_nice
	neferwl version
	neferwl validate-config /usr/share/doc/neferwl/config.example
	for lib in libinput.so.10 libseat.so.1 libudev.so.1 libxkbcommon.so.0 libvulkan.so.1; do
		ldconfig -p | grep -q "$lib" || { echo "missing $lib"; exit 1; }
	done
	apt-get remove -y -qq neferwl >/dev/null
'
