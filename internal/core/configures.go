package core

import "github.com/bnema/neferwl/internal/ports"

// configureTarget is where a placement is shown, as the configure needs it.
type configureTarget struct {
	output  string
	area    Rect // the output, to tell whether the window is on screen
	focused bool // activated: focused on the focused output
	// client is the placement minus borders; imposed reports core sized a
	// floating window (native floats pick their own size).
	client  Rect
	imposed bool
}

// configures owns the last configure sent to each window and decides the
// next one. Configures are sent only when they change.
type configures struct {
	sent map[WindowID]ports.ConfigureWindow
	seen map[WindowID]bool
}

func newConfigures() configures {
	return configures{sent: map[WindowID]ports.ConfigureWindow{}, seen: map[WindowID]bool{}}
}

// next returns the configure for p at t and whether it must be sent. The
// caller records it with mark once sent.
func (s *configures) next(p Placement, t configureTarget) (ports.ConfigureWindow, bool) {
	s.seen[p.ID] = true
	old, ok := s.sent[p.ID]
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
		v.Activated, v.Visible, v.Output = false, false, t.output
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
			v.Width, v.Height, v.Fullscreen, v.Floating = t.area.W, t.area.H, true, false
		}
		v.Activated, v.Visible, v.Output = t.focused, onScreen(p, t.area), t.output
	default:
		v = ports.ConfigureWindow{ID: p.ID, Width: t.client.W, Height: t.client.H, Fullscreen: p.Fullscreen, Activated: t.focused, Floating: p.Floating && !p.Fullscreen, Output: t.output, Visible: onScreen(p, t.area)}
		if v.Floating && !t.imposed {
			v.Width, v.Height = 0, 0
		}
	}
	return v, !ok || v != old
}

// mark records v as sent.
func (s *configures) mark(v ports.ConfigureWindow) { s.sent[v.ID] = v }

// prune forgets the windows no next call saw since the last prune: they
// left the layout.
func (s *configures) prune() {
	for id := range s.sent {
		if !s.seen[id] {
			delete(s.sent, id)
		}
	}
	clear(s.seen)
}
