package core

import (
	"errors"
	"fmt"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
)

// ErrRenderGPURemoved ends the session when the card of the render GPU is
// unplugged: the render device is not replaced at runtime. The session tears
// everything down in order and returns it, so the process exits non-zero
// and a supervisor can restart it.
var ErrRenderGPURemoved = errors.New("render GPU removed")

// HotplugKind is what runOutputs does with a hotplug event.
type HotplugKind int

const (
	HotplugConnector     HotplugKind = iota // rescan connectors
	HotplugCardAdded                        // a card other than the render card appeared
	HotplugCardRemoved                      // a card other than the render card went away
	HotplugRenderRemoved                    // the render card went away
)

// ClassifyHotplug is independent of recovery state: a render card removal
// must be seen whether or not the device is being reopened.
func ClassifyHotplug(ev domain.HotplugEvent, render domain.CardName) HotplugKind {
	if ev.Card == "" {
		return HotplugConnector
	}
	switch ev.Action {
	case domain.HotplugCardAdd:
		return HotplugCardAdded
	case domain.HotplugCardRemove:
		if ev.Card == render {
			return HotplugRenderRemoved
		}
		return HotplugCardRemoved
	}
	return HotplugConnector
}

// HotplugAddAction is the decision for an "add" event of a card.
type HotplugAddAction int

const (
	HotplugAddOpen     HotplugAddAction = iota // open it as a non-render card
	HotplugAddUnknown                          // not (yet) listed under /sys/class/drm
	HotplugAddExisting                         // already open: duplicate event
	HotplugAddDying                            // unplugged and still shutting down: retry after
	HotplugAddIgnored                          // filtered out by drm.devices / drm.ignore
	HotplugAddFailed                           // opening it failed; no retry until the next event
)

// RetryAfterTeardown reports whether an add is remembered until the dying
// card of the same name has finished. A failed open is not retried.
func (a HotplugAddAction) RetryAfterTeardown() bool { return a == HotplugAddDying }

// PlanHotplugAdd checks only the added card against the retained allow/ignore
// identifiers. Unlike startup selection, unrelated identifiers may be absent and
// no render GPU is selected. existing and dying are DRM card names.
func PlanHotplugAdd(cands []domain.GPUInfo, card domain.CardName, cfg GPUSelectConfig, existing, dying []domain.CardName) (domain.GPUInfo, HotplugAddAction, domain.GPUSelection, error) {
	i := slices.IndexFunc(cands, func(g domain.GPUInfo) bool { return g.Name == card })
	if i < 0 {
		return domain.GPUInfo{}, HotplugAddUnknown, domain.GPUSelection{}, nil
	}
	info := cands[i]
	switch {
	case slices.Contains(existing, card):
		return info, HotplugAddExisting, domain.GPUSelection{}, nil
	case slices.Contains(dying, card):
		return info, HotplugAddDying, domain.GPUSelection{}, nil
	}
	matches := func(ids []string) bool {
		return slices.ContainsFunc(ids, func(id string) bool { return matchesGPU(info, id) })
	}
	sel := domain.GPUSelection{Render: -1}
	reason := "all devices"
	action := HotplugAddOpen
	switch {
	case matches(cfg.Ignore):
		action, reason = HotplugAddIgnored, "ignore"
	case len(cfg.Devices) != 0 && !matches(cfg.Devices):
		action, reason = HotplugAddIgnored, "not in devices"
	case len(cfg.Devices) != 0:
		reason = "devices"
	}
	decision := "ignore"
	if action == HotplugAddOpen {
		decision = "keep"
		sel.Keep = []int{i}
	}
	sel.Reasons = []string{fmt.Sprintf("%s GPU %s (%s): %s", decision, info.Name, info.PCI, reason)}
	return info, action, sel, nil
}

// CardTeardown tracks an unplugged card until the outputs it owned have all
// stopped: only then may its GBM device, local render device and fd close.
type CardTeardown struct {
	left map[string]struct{}
}

func NewCardTeardown(outputs []string) *CardTeardown {
	t := &CardTeardown{left: make(map[string]struct{}, len(outputs))}
	for _, name := range outputs {
		t.left[name] = struct{}{}
	}
	return t
}

// Done reports whether no output is left.
func (t *CardTeardown) Done() bool { return len(t.left) == 0 }

// OutputStopped records that name stopped. It returns true exactly when this
// was the last awaited output; unknown or repeated names return false.
func (t *CardTeardown) OutputStopped(name string) bool {
	if _, ok := t.left[name]; !ok {
		return false
	}
	delete(t.left, name)
	return len(t.left) == 0
}

// PendingCards is an ordered set of card names waiting for a later retry.
type PendingCards struct{ names []domain.CardName }

func (p *PendingCards) Add(name domain.CardName) {
	if !slices.Contains(p.names, name) {
		p.names = append(p.names, name)
	}
}

func (p *PendingCards) Remove(name domain.CardName) {
	p.names = slices.DeleteFunc(p.names, func(n domain.CardName) bool { return n == name })
}

func (p *PendingCards) Has(name domain.CardName) bool { return slices.Contains(p.names, name) }

func (p *PendingCards) Take() []domain.CardName {
	names := p.names
	p.names = nil
	return names
}

// RetireOnStop says whether a stopped output's security instance is retired:
// on affirmative KMS inactivity, or when its card was removed and the device
// node is gone (a real unplug). A removed card whose node still exists, such
// as a driver unbind, is not proof of inactivity.
func RetireOnStop(inactiveOnClose, removed, nodeExists bool) bool {
	return inactiveOnClose || (removed && !nodeExists)
}
