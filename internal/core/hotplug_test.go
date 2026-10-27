package core

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/neferwl/internal/domain"
)

func TestHotplugClassify(t *testing.T) {
	const render = "card1"
	for _, tc := range []struct {
		name string
		ev   domain.HotplugEvent
		want HotplugKind
	}{
		{"connector change", domain.HotplugEvent{Action: domain.HotplugConnectorChange}, HotplugConnector},
		{"other card add", domain.HotplugEvent{Card: "card0", Action: domain.HotplugCardAdd}, HotplugCardAdded},
		{"render card add", domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardAdd}, HotplugCardAdded},
		{"other card remove", domain.HotplugEvent{Card: "card0", Action: domain.HotplugCardRemove}, HotplugCardRemoved},
		{"render card remove", domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardRemove}, HotplugRenderRemoved},
		{"prefix is not the render card", domain.HotplugEvent{Card: "card10", Action: domain.HotplugCardRemove}, HotplugCardRemoved},
		{"card change", domain.HotplugEvent{Card: "card1", Action: domain.HotplugConnectorChange}, HotplugConnector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyHotplug(tc.ev, render))
		})
	}
}

// Device ids are PCI addresses: path ids are resolved on the real filesystem.
func TestHotplugPlanAdd(t *testing.T) {
	cands := []domain.GPUInfo{
		{Name: "card0", Path: "/dev/dri/card0", PCI: "0000:03:00.0", Render: 1},
		{Name: "card1", Path: "/dev/dri/card1", PCI: "0000:00:02.0", Render: 2},
		{Name: "card2", Path: "/dev/dri/card2", PCI: "0000:04:00.0", Render: 3},
	}
	for _, tc := range []struct {
		name     string
		card     domain.CardName
		cfg      domain.Config
		env      string
		existing []domain.CardName
		dying    []domain.CardName
		want     HotplugAddAction
		wantErr  bool
	}{
		{"all devices kept", "card2", domain.Config{}, "", []domain.CardName{"card0", "card1"}, nil, HotplugAddOpen, false},
		{"duplicate add", "card2", domain.Config{}, "", []domain.CardName{"card2"}, nil, HotplugAddExisting, false},
		{"still dying", "card2", domain.Config{}, "", nil, []domain.CardName{"card2"}, HotplugAddDying, false},
		{"not listed yet", "card7", domain.Config{}, "", nil, nil, HotplugAddUnknown, false},
		{"ignored by pci", "card2", domain.Config{DRM: drmConfig(nil, []string{"0000:04:00.0"})}, "", nil, nil, HotplugAddIgnored, false},
		{"not in allow list", "card2", domain.Config{DRM: drmConfig([]string{"0000:03:00.0"}, nil)}, "", nil, nil, HotplugAddIgnored, false},
		{"in allow list", "card2", domain.Config{DRM: drmConfig([]string{"0000:03:00.0", "0000:04:00.0"}, nil)}, "", nil, nil, HotplugAddOpen, false},
		{"other card ignored does not matter", "card2", domain.Config{DRM: drmConfig(nil, []string{"0000:03:00.0"})}, "", nil, nil, HotplugAddOpen, false},
		{"selection error ignores the card", "card2", domain.Config{DRM: drmConfig(nil, []string{"0000:09:00.0"})}, "", nil, nil, HotplugAddIgnored, true},
		{"render env pointing elsewhere is fine", "card2", domain.Config{}, "0000:00:02.0", nil, nil, HotplugAddOpen, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, got, _, err := PlanHotplugAdd(cands, tc.card, GPUSelectConfig{Devices: tc.cfg.DRM.Devices, Ignore: tc.cfg.DRM.Ignore, Render: tc.env}, tc.existing, tc.dying)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantErr, err != nil)
			if got != HotplugAddUnknown {
				assert.Equal(t, "/dev/dri/"+string(tc.card), info.Path)
			}
		})
	}
}

func drmConfig(devices, ignore []string) (c struct{ Devices, Ignore []string }) {
	c.Devices, c.Ignore = devices, ignore
	return c
}

func TestHotplugCardTeardown(t *testing.T) {
	t.Run("no outputs is Done at once", func(t *testing.T) {
		assert.True(t, NewCardTeardown(nil).Done())
	})
	t.Run("last output finishes", func(t *testing.T) {
		td := NewCardTeardown([]string{"DP-1", "DP-2"})
		assert.False(t, td.Done())
		assert.False(t, td.OutputStopped("DP-1"))
		assert.False(t, td.OutputStopped("DP-1"), "repeat does not count")
		assert.False(t, td.OutputStopped("HDMI-A-1"), "foreign output does not count")
		assert.True(t, td.OutputStopped("DP-2"))
		assert.True(t, td.Done())
		assert.False(t, td.OutputStopped("DP-2"), "finishes only once")
	})
}

func TestHotplugPendingCards(t *testing.T) {
	var p PendingCards
	p.Add("card2")
	p.Add("card3")
	p.Add("card2")
	assert.True(t, p.Has("card3"))
	p.Remove("card3")
	assert.False(t, p.Has("card3"))
	p.Add("card4")
	assert.Equal(t, []domain.CardName{"card2", "card4"}, p.Take())
	assert.Empty(t, p.Take())
}

func TestHotplugAddRetryAfterTeardown(t *testing.T) {
	for action, want := range map[HotplugAddAction]bool{
		HotplugAddOpen: false, HotplugAddUnknown: false, HotplugAddExisting: false,
		HotplugAddDying: true, HotplugAddIgnored: false, HotplugAddFailed: false,
	} {
		assert.Equal(t, want, action.RetryAfterTeardown(), "action %d", action)
	}
}

func TestHotplugRetireOnStop(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		inactiveOnClose, removed, nodeExists bool
		want                                 bool
	}{
		{"kms confirmed", true, false, true, true},
		{"kms confirmed on removed card", true, true, true, true},
		{"unplug: removed, node gone", false, true, false, true},
		{"unbind: removed, node present", false, true, true, false},
		{"node missing but card not removed", false, false, false, false},
		{"plain error stop", false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RetireOnStop(tc.inactiveOnClose, tc.removed, tc.nodeExists))
		})
	}
}
