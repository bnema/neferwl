package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// ErrRenderGPURemoved ends the session when the card of the render GPU is
// unplugged: the render device is not replaced at runtime. The session tears
// everything down in order and returns it, so the process exits non-zero
// and a supervisor can restart it.
var ErrRenderGPURemoved = errors.New("render GPU removed")

// hotplugKind is what runOutputs does with a hotplug event.
type hotplugKind int

const (
	hotplugConnector     hotplugKind = iota // rescan connectors
	hotplugCardAdded                        // a card other than the render card appeared
	hotplugCardRemoved                      // a card other than the render card went away
	hotplugRenderRemoved                    // the render card went away
)

// classifyHotplug is independent of recovery state: a render card removal
// must be seen whether or not the device is being reopened.
func classifyHotplug(ev drm.HotplugEvent, renderPath string) hotplugKind {
	if ev.Card == "" {
		return hotplugConnector
	}
	switch ev.Action {
	case drm.HotplugAdd:
		return hotplugCardAdded
	case drm.HotplugRemove:
		if filepath.Join("/dev/dri", ev.Card) == renderPath {
			return hotplugRenderRemoved
		}
		return hotplugCardRemoved
	}
	return hotplugConnector
}

// hotplugAddAction is the decision for an "add" event of a card.
type hotplugAddAction int

const (
	hotplugAddOpen     hotplugAddAction = iota // open it as a non-render card
	hotplugAddUnknown                          // not (yet) listed under /sys/class/drm
	hotplugAddExisting                         // already open: duplicate event
	hotplugAddDying                            // unplugged and still shutting down: retry after
	hotplugAddIgnored                          // filtered out by drm.devices / drm.ignore
	hotplugAddFailed                           // opening it failed; no retry until the next event
)

// retryAfterTeardown reports whether an add is remembered until the dying
// card of the same name has finished. A failed open is not retried.
func (a hotplugAddAction) retryAfterTeardown() bool { return a == hotplugAddDying }

// planHotplugAdd applies the startup allow/ignore filter to a card that just
// appeared. The render GPU never changes at runtime, so the selector's own
// render choice is not used: every added card is a non-render card. existing
// and dying are device paths (/dev/dri/cardN).
func planHotplugAdd(cands []ports.GPUInfo, card string, cfg ports.Config, env string, existing, dying []string) (ports.GPUInfo, hotplugAddAction, ports.GPUSelection, error) {
	path := filepath.Join("/dev/dri", card)
	i := slices.IndexFunc(cands, func(g ports.GPUInfo) bool { return filepath.Base(g.Path) == card })
	if i < 0 {
		return ports.GPUInfo{}, hotplugAddUnknown, ports.GPUSelection{}, nil
	}
	info := cands[i]
	switch {
	case slices.Contains(existing, path):
		return info, hotplugAddExisting, ports.GPUSelection{}, nil
	case slices.Contains(dying, path):
		return info, hotplugAddDying, ports.GPUSelection{}, nil
	}
	_, sel, err := selectGPUPlans(cands, cfg, env)
	if err != nil {
		return info, hotplugAddIgnored, sel, err
	}
	if !slices.Contains(sel.Keep, i) {
		return info, hotplugAddIgnored, sel, nil
	}
	return info, hotplugAddOpen, sel, nil
}

// cardTeardown tracks an unplugged card until the outputs it owned have all
// stopped: only then may its GBM device, local render device and fd close.
type cardTeardown struct {
	left map[string]struct{}
}

func newCardTeardown(outputs []string) *cardTeardown {
	t := &cardTeardown{left: make(map[string]struct{}, len(outputs))}
	for _, name := range outputs {
		t.left[name] = struct{}{}
	}
	return t
}

// done reports whether no output is left.
func (t *cardTeardown) done() bool { return len(t.left) == 0 }

// outputStopped records that name stopped. It returns true exactly when this
// was the last awaited output; unknown or repeated names return false.
func (t *cardTeardown) outputStopped(name string) bool {
	if _, ok := t.left[name]; !ok {
		return false
	}
	delete(t.left, name)
	return len(t.left) == 0
}

// pendingCards is an ordered set of card names waiting for a later retry.
type pendingCards struct{ names []string }

func (p *pendingCards) add(name string) {
	if !slices.Contains(p.names, name) {
		p.names = append(p.names, name)
	}
}

func (p *pendingCards) remove(name string) {
	p.names = slices.DeleteFunc(p.names, func(n string) bool { return n == name })
}

func (p *pendingCards) has(name string) bool { return slices.Contains(p.names, name) }

func (p *pendingCards) take() []string {
	names := p.names
	p.names = nil
	return names
}

// readerFailure is the error of one card's ReadEvents goroutine.
type readerFailure struct {
	card *drmCard
	err  error
}

// hotplugAdd opens a card that just appeared, or returns nil with the action
// that explains why not. It runs on the runOutputs goroutine.
func (b *drmBackend) hotplugAdd(ctx context.Context, name string, want drm.Want) (*drmCard, hotplugAddAction) {
	log := b.log
	cands, err := drm.ListGPUs("/sys", "/dev")
	if err != nil {
		log.Warn().Err(err).Str("card", name).Msg("GPU hotplug: list GPUs")
		return nil, hotplugAddUnknown
	}
	var existing, dying []string
	for _, c := range b.cards {
		existing = append(existing, c.Path())
	}
	for c := range b.dying {
		dying = append(dying, c.Path())
	}
	info, action, sel, err := planHotplugAdd(cands, name, b.startupConfig, os.Getenv("NEFERWL_RENDER_DEVICE"), existing, dying)
	if err != nil {
		log.Warn().Err(err).Str("card", name).Msg("GPU hotplug: selection failed, card ignored")
	}
	switch action {
	case hotplugAddUnknown:
		log.Warn().Str("card", name).Msg("GPU hotplug: card not listed, ignored")
	case hotplugAddExisting:
		log.Info().Str("card", name).Msg("GPU hotplug: card already open")
	case hotplugAddDying:
		log.Info().Str("card", name).Msg("GPU hotplug: card still shutting down, will retry")
	case hotplugAddIgnored:
		log.Info().Str("card", name).Msg("GPU hotplug: card not selected by drm.devices/drm.ignore, not opened")
	case hotplugAddOpen:
		for _, reason := range sel.Reasons {
			log.Info().Msg(reason)
		}
		c, err := b.openCard(ctx, info, multiGPUMode(b.startupConfig.Render.MultiGPU), want, logging.Enabled(ctx, "render-damage"))
		if err != nil {
			log.Warn().Err(err).Str("card", name).Msg("GPU hotplug: card unusable, not retried")
			return nil, hotplugAddFailed
		}
		return c, action
	}
	return nil, action
}

// retireOnStop says whether a stopped output's security instance is retired:
// on affirmative KMS inactivity, or when its card was removed and the device
// node is gone (a real unplug). A removed card whose node still exists, such
// as a driver unbind, is not proof of inactivity.
func retireOnStop(inactiveOnClose, removed, nodeExists bool) bool {
	return inactiveOnClose || (removed && !nodeExists)
}
