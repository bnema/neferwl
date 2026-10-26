package drm

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

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

func TestHotplugDeliverPolicy(t *testing.T) {
	change := HotplugEvent{Action: "change"}
	add := HotplugEvent{Card: "card1", Action: "add"}
	remove := HotplugEvent{Card: "card1", Action: "remove"}

	t.Run("pending change is coalesced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan HotplugEvent, 1)
			assert.True(t, DeliverHotplug(t.Context(), ch, change))
			assert.False(t, DeliverHotplug(t.Context(), ch, change), "second change is dropped, not blocked")
			assert.Equal(t, change, <-ch)
		})
	})
	t.Run("card event blocks until taken", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan HotplugEvent, 1)
			ch <- change
			var delivered atomic.Bool
			go func() { delivered.Store(DeliverHotplug(t.Context(), ch, remove)) }()
			synctest.Wait()
			assert.False(t, delivered.Load(), "blocked on a full channel")
			assert.Equal(t, change, <-ch)
			synctest.Wait()
			assert.True(t, delivered.Load())
			assert.Equal(t, remove, <-ch)
		})
	})
	t.Run("card events keep their order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan HotplugEvent, 2)
			assert.True(t, DeliverHotplug(t.Context(), ch, add))
			assert.True(t, DeliverHotplug(t.Context(), ch, remove))
			assert.Equal(t, []HotplugEvent{add, remove}, []HotplugEvent{<-ch, <-ch})
		})
	})
	t.Run("context ends a blocked card send", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			ch := make(chan HotplugEvent)
			var delivered atomic.Bool
			delivered.Store(true)
			go func() { delivered.Store(DeliverHotplug(ctx, ch, add)) }()
			synctest.Wait()
			cancel()
			synctest.Wait()
			assert.False(t, delivered.Load())
		})
	})
}
