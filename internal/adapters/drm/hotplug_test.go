package drm

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
)

// parseUevent splits a raw uevent payload (NUL-separated KEY=VALUE entries,
// possibly after an "action@devpath" header) into properties, as libudev does.
func parseUevent(payload []byte) (domain.HotplugEvent, bool) {
	props := map[string]string{}
	for _, entry := range strings.Split(string(payload), "\x00") {
		if key, val, found := strings.Cut(entry, "="); found && key != "" {
			props[key] = val
		}
	}
	return hotplugFromProps(func(key string) string { return props[key] })
}

func uevent(entries ...string) []byte { return []byte(strings.Join(entries, "\x00") + "\x00") }

func TestHotplugFromProps(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    domain.HotplugEvent
		ok      bool
	}{
		{"card add", uevent("add@/devices/pci0000:00/0000:00:01.0/drm/card1", "ACTION=add", "DEVPATH=/devices/pci0000:00/0000:00:01.0/drm/card1", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1", "DEVTYPE=drm_minor"), domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardAdd}, true},
		{"card remove kernel devname", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVNAME=dri/card0", "DEVTYPE=drm_minor"), domain.HotplugEvent{Card: "card0", Action: domain.HotplugCardRemove}, true},
		{"card add devpath fallback", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card12"), domain.HotplugEvent{Card: "card12", Action: domain.HotplugCardAdd}, true},
		{"connector change", uevent("ACTION=change", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1", "HOTPLUG=1", "CONNECTOR=92"), domain.HotplugEvent{Action: domain.HotplugConnectorChange}, true},
		{"change without hotplug", uevent("ACTION=change", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1"), domain.HotplugEvent{}, false},
		{"render node add", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/renderD128", "DEVTYPE=drm_minor"), domain.HotplugEvent{}, false},
		{"render node remove", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVNAME=dri/renderD128"), domain.HotplugEvent{}, false},
		{"connector device add", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card1/card1-DP-2"), domain.HotplugEvent{}, false},
		{"connector device remove", uevent("ACTION=remove", "SUBSYSTEM=drm", "DEVPATH=/devices/x/drm/card1/card1-DP-2"), domain.HotplugEvent{}, false},
		{"non-drm subsystem", uevent("ACTION=add", "SUBSYSTEM=usb", "DEVNAME=/dev/dri/card1"), domain.HotplugEvent{}, false},
		{"non-drm hotplug", uevent("ACTION=change", "SUBSYSTEM=input", "HOTPLUG=1"), domain.HotplugEvent{}, false},
		{"unrelated action", uevent("ACTION=bind", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1"), domain.HotplugEvent{}, false},
		{"card without number", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card"), domain.HotplugEvent{}, false},
		{"card with suffix", uevent("ACTION=add", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card1x"), domain.HotplugEvent{}, false},
		{"empty", nil, domain.HotplugEvent{}, false},
		{"garbage", []byte("\x00\x00=\x00noequals"), domain.HotplugEvent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseUevent(tt.payload)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHotplugDeliverPolicy(t *testing.T) {
	change := domain.HotplugEvent{Action: domain.HotplugConnectorChange}
	add := domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardAdd}
	remove := domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardRemove}

	t.Run("pending change is coalesced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan domain.HotplugEvent, 1)
			assert.True(t, DeliverHotplug(t.Context(), ch, change))
			assert.False(t, DeliverHotplug(t.Context(), ch, change), "second change is dropped, not blocked")
			assert.Equal(t, change, <-ch)
		})
	})
	t.Run("card event blocks until taken", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ch := make(chan domain.HotplugEvent, 1)
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
			ch := make(chan domain.HotplugEvent, 2)
			assert.True(t, DeliverHotplug(t.Context(), ch, add))
			assert.True(t, DeliverHotplug(t.Context(), ch, remove))
			assert.Equal(t, []domain.HotplugEvent{add, remove}, []domain.HotplugEvent{<-ch, <-ch})
		})
	})
	t.Run("context ends a blocked card send", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			ch := make(chan domain.HotplugEvent)
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
