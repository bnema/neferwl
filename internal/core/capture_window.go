package core

import "github.com/bnema/neferwl/internal/ports"

// Window capture: a session of one window (CaptureSessionOpen.Window) renders
// it off screen, alone, at its client size, wherever it is: on screen, on
// another workspace, under a fullscreen window or in a hidden stash. While it
// is captured and not on screen its configure says Captured, so the client
// keeps drawing (not suspended). The scene it is drawn from is
// Scene.CaptureScene of the output that owns it, like a hidden workspace; one
// target, a workspace or a window, is rendered off screen at a time.

// windowSize is a window's client size, at the origin: its placement on its
// own workspace or, when that hides it (a stashed or covered float), the
// float's own size, else the size it was last configured to.
func (c *Core) windowSize(w *Workspace, id WindowID) Rect {
	for _, p := range w.Layout() {
		if p.ID == id && !p.Hidden && p.Rect.W > 0 && p.Rect.H > 0 {
			r := c.clientRect(p)
			return Rect{W: r.W, H: r.H}
		}
	}
	for _, fs := range [][]Float{w.Floats, w.Stash} {
		for _, f := range fs {
			if f.ID == id && f.W > 0 && f.H > 0 {
				return Rect{W: f.W, H: f.H}
			}
		}
	}
	if v, ok := c.configures.sent[id]; ok && v.Width > 0 && v.Height > 0 {
		return Rect{W: v.Width, H: v.Height}
	}
	return Rect{}
}

// popupOffset is where a popup lies relative to the client area of its root
// window.
func (c *Core) popupOffset(id WindowID) Rect {
	p := c.popups[id]
	if p == nil {
		return Rect{}
	}
	r := p.rect
	if c.popups[p.parent] != nil {
		o := c.popupOffset(p.parent)
		r.X, r.Y = r.X+o.X, r.Y+o.Y
	}
	return r
}

// captureWindowScene is the window of v drawn for capture only: a scene of its
// client size with the window at the origin and its mapped popups over it.
func (c *Core) captureWindowScene(v *capView, seq uint64) *ports.Scene {
	sz := v.windowSz
	s := &ports.Scene{
		Security: c.security,
		Output:   v.windowScr.name(), Seq: seq, OutputWidth: sz.W, OutputHeight: sz.H, WorkspaceClip: Rect{W: sz.W, H: sz.H},
		Scale: v.windowScr.scale, Background: c.cfg.Background.Color,
		Border:  ports.Border{Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive},
		Windows: []ports.SceneWindow{{ID: v.window, Rect: Rect{W: sz.W, H: sz.H}, Fullscreen: true}},
	}
	for _, id := range c.popupOrder {
		if p := c.popups[id]; p != nil && p.mapped && c.popupRoot(id) == v.window {
			s.Windows = append(s.Windows, ports.SceneWindow{ID: id, Rect: c.popupOffset(id), Popup: true})
		}
	}
	return s
}
