package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// captureWorkspace is the workspace a capture session renders off screen
// while it is not the one on screen. Its windows keep a real layout in the
// workspace viewport: they are sized (configure Captured) and drawn into
// Scene.CaptureScene, but they are hidden on the physical layout, so they
// take no pointer input and no physical presentation feedback.
//
// Only the core goroutine touches it. The index map and its storage are
// reused by every publish (cleared, never reallocated once warm).
type captureWorkspace struct {
	sc         *screen
	ws         *Workspace // nil: no workspace is captured off screen
	frame      Rect       // ws.Output: the viewport, in monitor coordinates
	placements []Placement
	index      map[WindowID]int
}

// active reports whether a hidden workspace is captured.
func (cw *captureWorkspace) active() bool { return cw != nil && cw.ws != nil }

// reset forgets the captured workspace, keeping the storage.
func (cw *captureWorkspace) reset() {
	cw.sc, cw.ws, cw.frame = nil, nil, Rect{}
	cw.placements = nil
	clear(cw.index)
}

// load lays out ws in its own viewport. ws.Layout allocates the placements
// (the baseline of the existing layout); the index reuses its map.
func (cw *captureWorkspace) load(sc *screen, ws *Workspace) {
	cw.sc, cw.ws, cw.frame = sc, ws, ws.Output
	cw.placements = ws.Layout()
	if cw.index == nil {
		cw.index = make(map[WindowID]int, len(cw.placements))
	}
	clear(cw.index)
	for i := range cw.placements {
		cw.index[cw.placements[i].ID] = i
	}
}

// placement is the real placement of a window of the captured workspace.
func (cw *captureWorkspace) placement(id WindowID) *Placement {
	if !cw.active() {
		return nil
	}
	if i, ok := cw.index[id]; ok {
		return &cw.placements[i]
	}
	return nil
}

// captureTrack follows the session view of this publish: it lays out the
// captured workspace when it is not drawn as itself (another workspace is
// on screen, or the overview shows it as a preview) and forgets it
// otherwise. It runs
// right after captureEvaluate, before the popups and configures that use it.
func (c *Core) captureTrack(view *capView) {
	// Tracking follows the decided view, not whether frames were taken: while
	// the captured workspace slides in its own viewport its windows stay
	// captured (not suspended) and its popups stay.
	if view == nil || view.hidden == nil {
		c.configures.cw.reset()
		return
	}
	c.configures.cw.load(view.hiddenScr, view.hidden)
}

// captureConfigure picks how a physical placement p on sc is configured:
// for a window of the captured workspace it returns the real placement and
// the target sized for it, else nil and t unchanged. Pass both to
// configures.nextWithCapture.
func (c *Core) captureConfigure(sc *screen, p Placement, t configureTarget) (*Placement, configureTarget) {
	cw := &c.configures.cw
	if !cw.active() || cw.sc != sc {
		return nil, t
	}
	cp := cw.placement(p.ID)
	if cp == nil {
		return nil, t
	}
	t.captureArea = cw.frame
	if !p.Hidden {
		// An overview preview keeps its real size and physical state.
		return cp, t
	}
	t.area = cw.frame
	t.focused = false
	t.client, t.imposed = Rect{}, false
	if !cp.Hidden {
		t.client, t.imposed = c.clientRect(*cp), cw.ws.imposedFloat(cp.ID)
	}
	return cp, t
}

// captureRect is where a window of the captured workspace, or a popup
// hanging from one, is: its client area in monitor coordinates. Unlike
// windowRect it knows hidden workspaces; it never serves input. A stale
// captured workspace (its screen or workspace gone) has no geometry.
func (c *Core) captureRect(id WindowID) (*screen, Rect, bool) {
	cw := &c.configures.cw
	if !cw.active() || !slices.Contains(c.screens, cw.sc) || !cw.sc.mon.has(cw.ws) {
		return nil, Rect{}, false
	}
	if p := c.popups[id]; p != nil {
		sc, pr, ok := c.captureRect(p.parent)
		if !ok {
			return nil, Rect{}, false
		}
		if cp := cw.placement(c.popupRoot(id)); cp != nil && cp.Peek {
			return nil, Rect{}, false
		}
		return sc, Rect{X: pr.X + p.rect.X, Y: pr.Y + p.rect.Y, W: p.rect.W, H: p.rect.H}, true
	}
	cp := cw.placement(id)
	if cp == nil || cp.Hidden || !cp.Rect.Overlaps(cw.frame) {
		return nil, Rect{}, false
	}
	return cw.sc, c.clientRect(*cp), true
}

// popupRect is windowRect that also knows the captured workspace; physical
// tells which one answered (a single physical lookup).
func (c *Core) popupRect(id WindowID) (sc *screen, r Rect, physical, ok bool) {
	if sc, r, ok := c.windowRect(id); ok {
		return sc, r, true, true
	}
	sc, r, ok = c.captureRect(id)
	return sc, r, false, ok
}

// captureScene is the captured workspace drawn for capture only: a scene the
// size of its viewport, whose windows and lines are rebased on the viewport
// origin. It has no layers and no capture of its own. nil without a
// captured workspace.
func (c *Core) captureScene(seq uint64) *ports.Scene {
	cw := &c.configures.cw
	if !cw.active() {
		return nil
	}
	f := cw.frame
	rebase := func(r Rect) Rect { r.X, r.Y = r.X-f.X, r.Y-f.Y; return r }
	s := &ports.Scene{
		Security: c.security,
		Output:   cw.sc.name(), Seq: seq, OutputWidth: f.W, OutputHeight: f.H, WorkspaceClip: Rect{W: f.W, H: f.H},
		Scale: cw.sc.scale, Background: c.cfg.Background.Color,
		Border:  ports.Border{Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive},
		Windows: make([]ports.SceneWindow, 0, len(cw.placements)),
		Dim:     floatDim(cw.placements, f, c.cfg.Floating.Dim),
	}
	// A hidden workspace lights nothing: its focus is not shown.
	for _, p := range cw.placements {
		w := ports.SceneWindow{ID: p.ID, Focused: false, Fullscreen: p.Fullscreen, Hidden: p.Hidden, Floating: p.Floating, Below: p.Below, Inset: p.Inset, Preview: p.Preview}
		if !p.Hidden {
			w.Rect = rebase(p.Rect)
		}
		if p.Peek {
			w.Dim = c.cfg.Stash.Dim
		}
		s.Windows = append(s.Windows, w)
	}
	s.Separators = separators(cw.placements, c.cfg.Border.Width, cw.ws.gap(), f, false)
	for i := range s.Separators {
		s.Separators[i].Rect = rebase(s.Separators[i].Rect)
	}
	for _, id := range c.popupOrder {
		p := c.popups[id]
		if p == nil || !p.mapped {
			continue
		}
		if _, r, ok := c.captureRect(id); ok {
			s.Windows = append(s.Windows, ports.SceneWindow{ID: id, Rect: rebase(r), Popup: true})
		}
	}
	return s
}
