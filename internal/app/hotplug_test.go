package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/ports"
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

// Device ids are PCI addresses: path ids are resolved on the real filesystem.
func TestHotplugPlanAdd(t *testing.T) {
	cands := []ports.GPUInfo{
		{Path: "/dev/dri/card0", PCI: "0000:03:00.0", Render: 1},
		{Path: "/dev/dri/card1", PCI: "0000:00:02.0", Render: 2},
		{Path: "/dev/dri/card2", PCI: "0000:04:00.0", Render: 3},
	}
	for _, tc := range []struct {
		name     string
		card     string
		cfg      ports.Config
		env      string
		existing []string
		dying    []string
		want     hotplugAddAction
		wantErr  bool
	}{
		{"all devices kept", "card2", ports.Config{}, "", []string{"/dev/dri/card0", "/dev/dri/card1"}, nil, hotplugAddOpen, false},
		{"duplicate add", "card2", ports.Config{}, "", []string{"/dev/dri/card2"}, nil, hotplugAddExisting, false},
		{"still dying", "card2", ports.Config{}, "", nil, []string{"/dev/dri/card2"}, hotplugAddDying, false},
		{"not listed yet", "card7", ports.Config{}, "", nil, nil, hotplugAddUnknown, false},
		{"ignored by pci", "card2", ports.Config{DRM: drmConfig(nil, []string{"0000:04:00.0"})}, "", nil, nil, hotplugAddIgnored, false},
		{"not in allow list", "card2", ports.Config{DRM: drmConfig([]string{"0000:03:00.0"}, nil)}, "", nil, nil, hotplugAddIgnored, false},
		{"in allow list", "card2", ports.Config{DRM: drmConfig([]string{"0000:03:00.0", "0000:04:00.0"}, nil)}, "", nil, nil, hotplugAddOpen, false},
		{"other card ignored does not matter", "card2", ports.Config{DRM: drmConfig(nil, []string{"0000:03:00.0"})}, "", nil, nil, hotplugAddOpen, false},
		{"selection error ignores the card", "card2", ports.Config{DRM: drmConfig(nil, []string{"0000:09:00.0"})}, "", nil, nil, hotplugAddIgnored, true},
		{"render env pointing elsewhere is fine", "card2", ports.Config{}, "0000:00:02.0", nil, nil, hotplugAddOpen, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, got, _, err := planHotplugAdd(cands, tc.card, tc.cfg, tc.env, tc.existing, tc.dying)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantErr, err != nil)
			if got != hotplugAddUnknown {
				assert.Equal(t, "/dev/dri/"+tc.card, info.Path)
			}
		})
	}
}

func drmConfig(devices, ignore []string) (c struct{ Devices, Ignore []string }) {
	c.Devices, c.Ignore = devices, ignore
	return c
}

func TestHotplugCardTeardown(t *testing.T) {
	t.Run("no outputs is done at once", func(t *testing.T) {
		assert.True(t, newCardTeardown(nil).done())
	})
	t.Run("last output finishes", func(t *testing.T) {
		td := newCardTeardown([]string{"DP-1", "DP-2"})
		assert.False(t, td.done())
		assert.False(t, td.outputStopped("DP-1"))
		assert.False(t, td.outputStopped("DP-1"), "repeat does not count")
		assert.False(t, td.outputStopped("HDMI-A-1"), "foreign output does not count")
		assert.True(t, td.outputStopped("DP-2"))
		assert.True(t, td.done())
		assert.False(t, td.outputStopped("DP-2"), "finishes only once")
	})
}

func TestHotplugPendingCards(t *testing.T) {
	var p pendingCards
	p.add("card2")
	p.add("card3")
	p.add("card2")
	assert.True(t, p.has("card3"))
	p.remove("card3")
	assert.False(t, p.has("card3"))
	p.add("card4")
	assert.Equal(t, []string{"card2", "card4"}, p.take())
	assert.Empty(t, p.take())
}

func TestHotplugAddRetryAfterTeardown(t *testing.T) {
	for action, want := range map[hotplugAddAction]bool{
		hotplugAddOpen: false, hotplugAddUnknown: false, hotplugAddExisting: false,
		hotplugAddDying: true, hotplugAddIgnored: false, hotplugAddFailed: false,
	} {
		assert.Equal(t, want, action.retryAfterTeardown(), "action %d", action)
	}
}
