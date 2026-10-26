package app

import (
	"errors"
	"path/filepath"

	"github.com/bnema/neferwl/internal/adapters/drm"
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
