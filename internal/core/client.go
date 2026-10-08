package core

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// clientEvent applies one client event. publish asks for a new scene;
// stop ends Run (a command could not be delivered).
func (c *Core) clientEvent(ctx context.Context, ev ports.ClientEvent) (publish, stop bool) {
	switch v := ev.(type) {
	case ports.SessionLockChanged:
		if !c.applyLockChanged(v) {
			return false, false
		}
	case ports.CaptureSessionOpen:
		c.captureOpen(v)
	case ports.CaptureSessionClose:
		c.captureClose(v.ID)
	case ports.CaptureFrameTaken:
		// Nothing new to show (the target flashes already): no scene.
		if !c.captureFrame(v) {
			return false, false
		}
	case ports.CaptureExclusionBegin:
		c.captureExclusionBegin(v)
	case ports.CaptureExclusionLayer:
		c.captureExclusionLayer(v)
	case ports.CaptureExclusionEnd:
		c.captureExclusionEnd(v.Session)
	case ports.LayerChanged:
		c.layerChanged = true
		c.setLayers(v.Layers)
	case ports.InputRegionChanged:
		c.windows.setRegion(v)
	case ports.WindowMapped:
		c.mapWindow(v)
	case ports.WindowResized:
		c.resizeFloating(v)
	case ports.PopupRequest:
		if err := c.placePopup(ctx, v); err != nil {
			return false, true
		}
	case ports.PopupMapped:
		if p := c.popups[v.ID]; p != nil {
			p.mapped = true
		}
	case ports.ShortcutsInhibit:
		c.windows.setInhibitShortcuts(v.Window, v.Active)
		if err := c.updateInhibit(ctx); err != nil {
			return false, true
		}
		return false, false
	case ports.OutputPower:
		if i := c.screenIndex(v.Output); i >= 0 && c.screens[i].off == v.On {
			c.screens[i].off = !v.On
		}
	case ports.IdleInhibit:
		c.windows.setIdleInhibit(v.Window, v.Active)
		c.publishState()
		return false, false
	case ports.WindowAppID:
		c.windows.setAppID(v.ID, v.AppID)
	case ports.WindowParent:
		if _, w := c.screenOf(v.ID); w != nil {
			w.SetDialogParent(v.ID, v.Parent)
		}
	case ports.WindowUnmapped:
		if c.unmapClient(ctx, v) != nil {
			return false, true
		}
	case ports.PointerConstrained:
		c.constrained = v
	case ports.PointerWarp:
		if c.warpPointer(ctx, v) != nil {
			return false, true
		}
	case ports.WindowMoveRequest:
		if c.clientDrag(ctx, v) != nil {
			return false, true
		}
	case ports.WindowFullscreenRequest:
		if c.fullscreenRequest(ctx, v) != nil {
			return false, true
		}
	case ports.WorkspaceActivate:
		if c.activateWorkspaces(ctx, v) != nil {
			return false, true
		}
	case ports.WindowActivate:
		if c.activate(ctx, v.ID) != nil {
			return false, true
		}
	}
	return true, false
}

// unmapClient drops a window or popup the client unmapped.
func (c *Core) unmapClient(ctx context.Context, v ports.WindowUnmapped) error {
	if c.popups[v.ID] != nil {
		if err := c.dropPopup(ctx, v.ID); err != nil {
			return err
		}
	}
	if err := c.closePopupsOf(ctx, v.ID); err != nil {
		return err
	}
	c.windows.drop(v.ID)
	if c.drag != nil && c.drag.id == v.ID {
		c.abortDrag()
	}
	c.unmapWindow(v)
	c.releaseSlots()
	if c.pointer == v.ID {
		c.pointer = 0
		if err := c.pointerFocus(ctx, ports.PointerFocus{}); err != nil {
			return err
		}
	}
	return nil
}

// fullscreenRequest answers xdg_toplevel set/unset_fullscreen, from the
// window itself or a taskbar (External).
func (c *Core) fullscreenRequest(ctx context.Context, v ports.WindowFullscreenRequest) error {
	c.configures.answer[v.ID] = true
	if v.Fullscreen && !v.External && c.now().Sub(c.windows.lookup(v.ID).mappedAt) < fullscreenGrace {
		return nil
	}
	// A taskbar request is a user action on that window, as an
	// activation: it comes on screen with the focus, leaving
	// another window's fullscreen.
	if v.External && v.Fullscreen {
		if err := c.activate(ctx, v.ID); err != nil {
			return err
		}
	}
	if s, _ := c.screenOf(v.ID); s != nil {
		s.mon.SetFullscreen(v.ID, v.Fullscreen)
		if w := s.mon.Current(); v.Fullscreen && w.fullscreen == v.ID {
			// A taskbar or late fullscreen request that took
			// effect: the window does not fade in, it keeps the
			// direct scanout path. One that was ignored (the
			// grace after the map) leaves the entrance running.
			delete(s.rects, v.ID)
		}
	}
	return nil
}

// activateWorkspaces shows the workspaces of v by stable ID, in order.
func (c *Core) activateWorkspaces(ctx context.Context, v ports.WorkspaceActivate) error {
	before := c.cur().mon.Current()
	for _, id := range v.IDs {
		for i, sc := range c.screens {
			if sc.name() == "" {
				continue
			}
			for w := range sc.mon.all() {
				if w.ID == id {
					c.focusScreen = i
					sc.mon.show(w)
					break
				}
			}
		}
	}
	return c.workspaceVisible(ctx, c.cur().mon.Current() != before)
}
