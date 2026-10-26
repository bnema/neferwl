package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/neferwl/internal/adapters/drm"
)

func TestHotplugClassify(t *testing.T) {
	const render = "/dev/dri/card1"
	for _, tc := range []struct {
		name string
		ev   drm.HotplugEvent
		want hotplugKind
	}{
		{"connector change", drm.HotplugEvent{Action: "change"}, hotplugConnector},
		{"other card add", drm.HotplugEvent{Card: "card0", Action: "add"}, hotplugCardAdded},
		{"render card add", drm.HotplugEvent{Card: "card1", Action: "add"}, hotplugCardAdded},
		{"other card remove", drm.HotplugEvent{Card: "card0", Action: "remove"}, hotplugCardRemoved},
		{"render card remove", drm.HotplugEvent{Card: "card1", Action: "remove"}, hotplugRenderRemoved},
		{"prefix is not the render card", drm.HotplugEvent{Card: "card10", Action: "remove"}, hotplugCardRemoved},
		{"card change", drm.HotplugEvent{Card: "card1", Action: "change"}, hotplugConnector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyHotplug(tc.ev, render))
		})
	}
}
