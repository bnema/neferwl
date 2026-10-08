package core

import "github.com/bnema/neferwl/internal/ports"

// configureTarget is where a placement is shown, as the configure needs it.
type configureTarget struct {
	output  string
	view    viewport // where the window is seen, to tell whether it is
	focused bool     // activated: focused on the focused output
	// client is the placement minus borders; imposed reports core sized a
	// floating window (native floats pick their own size).
	client  Rect
	imposed bool
	// captureArea is the viewport of the workspace a capture session renders
	// off screen, when this window belongs to it (captureConfigure).
	captureArea Rect
	// realTiled sizes a visible tiled preview from its real client rect.
	realTiled bool
	// captured: a capture session renders this window off screen (window
	// capture), so off screen it is Captured, not suspended.
	captured bool
}

// configures owns the last configure sent to each window and decides the
// next one. Configures are sent only when they change.
type configures struct {
	sent map[WindowID]ports.ConfigureWindow
	seen map[WindowID]bool
	// answer holds windows whose state request awaits a configure, sent
	// even unchanged: xdg-shell answers every request, a refused one too.
	answer map[WindowID]bool
	// cw is the workspace a capture session renders off screen, if any.
	cw captureWorkspace
}

func newConfigures() configures {
	return configures{sent: map[WindowID]ports.ConfigureWindow{}, seen: map[WindowID]bool{}, answer: map[WindowID]bool{}}
}

// next returns the configure for p at t and whether it must be sent. The
// caller records it with mark once sent.
func (s *configures) next(p Placement, t configureTarget) (ports.ConfigureWindow, bool) {
	s.seen[p.ID] = true
	old, ok := s.sent[p.ID]
	v := build(p, t, old, ok)
	v.Captured = t.captured && !v.Visible
	if t.captured && p.Hidden && t.client.W > 0 && t.client.H > 0 {
		// Hidden but captured: sized for the capture, which follows its real
		// layout.
		v.Width, v.Height = t.client.W, t.client.H
	}
	return v, !ok || v != old || s.answer[p.ID]
}

// nextWithCapture is next for a window of a workspace a capture session
// renders off screen. p is its placement on the physical layout; cp is its
// real placement on its own workspace, laid out in the viewport
// t.captureArea (nil: not captured, exactly next).
//
// A hidden p is sized from cp like a shown window and stays physically
// invisible and deactivated (Visible false). Any other p (an overview
// preview) is configured as usual, keeping its real size; only when the
// display does not show it does it get Captured, so a capture that draws it
// does not see it suspended. Captured is set when cp is drawn in the
// viewport.
func (s *configures) nextWithCapture(p Placement, t configureTarget, cp *Placement) (ports.ConfigureWindow, bool) {
	if cp == nil {
		return s.next(p, t)
	}
	s.seen[p.ID] = true
	old, ok := s.sent[p.ID]
	var v ports.ConfigureWindow
	if p.Hidden {
		t.focused = false
		v = build(*cp, t, old, ok)
		v.Activated, v.Visible = false, false
	} else {
		v = build(p, t, old, ok)
	}
	v.Captured = !v.Visible && (t.captured || !cp.Hidden && cp.Rect.Overlaps(t.captureArea))
	return v, !ok || v != old || s.answer[p.ID]
}

// build is the configure for p at t given the last one sent, if any. It
// never sets Captured: only nextWithCapture does, so a window that leaves a
// capture session drops the flag with its next configure.
func build(p Placement, t configureTarget, old ports.ConfigureWindow, ok bool) ports.ConfigureWindow {
	var v ports.ConfigureWindow
	switch {
	case p.Hidden:
		// A hidden window keeps its size, state and output; it is only
		// deactivated and marked invisible. One mapped hidden is told so
		// at once, sized by the client.
		v = old
		if !ok {
			v = ports.ConfigureWindow{ID: p.ID, Floating: p.Floating}
		}
		v.Activated, v.Visible, v.Output, v.Captured = false, false, t.output, false
	case p.Preview > 0:
		// A preview keeps its client's size: only its focus changes.
		v = old
		if !ok {
			v = ports.ConfigureWindow{ID: p.ID, Width: int(float64(p.Rect.W) / p.Preview), Height: int(float64(p.Rect.H) / p.Preview), Floating: p.Floating}
			if p.Floating {
				// A native float picks its own size.
				v.Width, v.Height = 0, 0
			}
		}
		if p.Fullscreen && !v.Fullscreen {
			// A float made fullscreen off screen: its preview row shows
			// it fullscreen, sized for the output, never the preview.
			v.Width, v.Height, v.Fullscreen, v.Floating = t.view.frame.W, t.view.frame.H, true, false
		}
		if t.realTiled {
			v.Width, v.Height = t.client.W, t.client.H
		}
		v.Activated, v.Visible, v.Output, v.Captured = t.focused, t.view.shows(p), t.output, false
	default:
		v = ports.ConfigureWindow{ID: p.ID, Width: t.client.W, Height: t.client.H, Fullscreen: p.Fullscreen, Activated: t.focused, Floating: p.Floating && !p.Fullscreen, Output: t.output, Visible: t.view.shows(p)}
		if v.Floating && !t.imposed {
			v.Width, v.Height = 0, 0
		}
	}
	return v
}

// mark records v as sent.
func (s *configures) mark(v ports.ConfigureWindow) {
	s.sent[v.ID] = v
	delete(s.answer, v.ID)
}

// keep marks id as seen without a configure: a window drawn while it leaves
// keeps what was sent to it. Only for a window the layout still holds (a
// hidden stash window): a destroyed one is forgotten (forget).
func (s *configures) keep(id WindowID) { s.seen[id] = true }

// forget drops what was sent to id: the window is unmapped. A toplevel
// that maps again keeps its ID, so it must start from nothing, or an
// unchanged first configure would be withheld.
func (s *configures) forget(id WindowID) {
	delete(s.sent, id)
	delete(s.answer, id)
	delete(s.seen, id)
}

// prune forgets the windows no next call saw since the last prune: they
// left the layout.
func (s *configures) prune() {
	for id := range s.sent {
		if !s.seen[id] {
			delete(s.sent, id)
		}
	}
	for id := range s.answer {
		if !s.seen[id] {
			delete(s.answer, id)
		}
	}
	clear(s.seen)
}
