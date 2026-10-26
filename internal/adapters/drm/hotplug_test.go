package drm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func uevent(entries ...string) []byte { return []byte(strings.Join(entries, "\x00") + "\x00") }

func TestHotplugParseUevent(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    HotplugEvent
		ok      bool
	}{
		{"card add", uevent("add@/devices/pci0000:00/0000:00:01.0/drm/card1", "ACTION=add", "DEVPATH=/devices/pci0000:00/0000:00:01.0/drm/card1", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1", "DEVTYPE=drm_minor"), HotplugEvent{Card: "card1", Action: "add"}, true},
		{"card remove kernel devname", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVNAME=dri/card0", "DEVTYPE=drm_minor"), HotplugEvent{Card: "card0", Action: "remove"}, true},
		{"card add devpath fallback", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card12"), HotplugEvent{Card: "card12", Action: "add"}, true},
		{"connector change", uevent("ACTION=change", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1", "HOTPLUG=1", "CONNECTOR=92"), HotplugEvent{Action: "change"}, true},
		{"change without hotplug", uevent("ACTION=change", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1"), HotplugEvent{}, false},
		{"render node add", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/renderD128", "DEVTYPE=drm_minor"), HotplugEvent{}, false},
		{"render node remove", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVNAME=dri/renderD128"), HotplugEvent{}, false},
		{"connector device add", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card1/card1-DP-2"), HotplugEvent{}, false},
		{"connector device remove", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card1/card1-DP-2"), HotplugEvent{}, false},
		{"non-drm subsystem", uevent("ACTION=add", "SUBSYSTEM=usb", "DEVNAME=/dev/dri/card1"), HotplugEvent{}, false},
		{"non-drm hotplug", uevent("ACTION=change", "SUBSYSTEM=input", "HOTPLUG=1"), HotplugEvent{}, false},
		{"unrelated action", uevent("ACTION=bind", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1"), HotplugEvent{}, false},
		{"card without number", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card"), HotplugEvent{}, false},
		{"card with suffix", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1x"), HotplugEvent{}, false},
		{"empty", nil, HotplugEvent{}, false},
		{"garbage", []byte("\x00\x00=\x00noequals"), HotplugEvent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseUevent(tt.payload)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
