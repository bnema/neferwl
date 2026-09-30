package capture

import (
	"errors"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// ErrSessionInactive fails a Clean request whose session is not the one the
// scene carries. Serving it as a normal capture would leak the session's
// HUD, its native border, or a workspace it no longer targets.
var ErrSessionInactive = errors.New("capture session is not active")

// ErrSessionStale fails a Clean request stamped with a session revision newer
// than the scene's: the scene predates a change of what a clean capture must
// hide (a new HUD layer, popup, target), so its exclusion list could leak.
var ErrSessionStale = errors.New("capture session scene is older than the request")

// cleaner derives the scene of clean captures: the frame without the native
// session border and without the excluded layers and popups. Its scratch
// slices are reused, so a warmed output allocates nothing.
type cleaner struct {
	windows []ports.SceneWindow
	layers  []ports.SceneLayer
}

// Shows reports whether a content change of id needs a new frame: the
// displayed scene draws it, or the hidden workspace of a capture session does
// (its child render and the reads it releases). Flips and Shown still count
// the displayed scene only.
func Shows(s ports.Scene, id ports.WindowID) bool {
	return s.Shows(id) || s.CaptureScene != nil && s.CaptureScene.Shows(id)
}

// BorderVisible reports whether the scene's native session border draws at
// least one pixel: a color, and a width that survives the renderer's clamp to
// half the smaller side of the target. The renderer draws by the same rule.
func BorderVisible(s ports.Scene) bool {
	c := s.Capture
	return c != nil && c.BorderColor != "" && min(c.BorderWidth, c.TargetRect.W/2, c.TargetRect.H/2) > 0
}

// cleanDiffers reports whether the clean scene differs from the displayed
// one: a native border is drawn, or an excluded surface is in the scene.
// When it does not, clean requests are served from the displayed frame.
func cleanDiffers(s ports.Scene) bool {
	c := s.Capture
	if c == nil {
		return false
	}
	if BorderVisible(s) {
		return true
	}
	if len(c.Excluded) == 0 {
		return false
	}
	for _, w := range s.Windows {
		if slices.Contains(c.Excluded, w.ID) {
			return true
		}
	}
	for _, l := range s.Layers {
		if slices.Contains(c.Excluded, l.ID) {
			return true
		}
	}
	return false
}

// scene returns s without Capture (so no border) and without the excluded
// windows (popups included) and layers. Seq is 0, so the renderer redraws
// the whole target instead of reusing a displayed frame. The result is valid
// until the next call. Without a session s is returned unchanged.
func (c *cleaner) scene(s ports.Scene) ports.Scene {
	if s.Capture == nil {
		return s
	}
	ex := s.Capture.Excluded
	s.Capture, s.Seq = nil, 0
	if len(ex) == 0 {
		return s
	}
	c.windows = c.windows[:0]
	for _, w := range s.Windows {
		if !slices.Contains(ex, w.ID) {
			c.windows = append(c.windows, w)
		}
	}
	c.layers = c.layers[:0]
	for _, l := range s.Layers {
		if !slices.Contains(ex, l.ID) {
			c.layers = append(c.layers, l)
		}
	}
	s.Windows, s.Layers = c.windows, c.layers
	return s
}

// CleanScene is the scene a Clean capture is taken from; see Split.
func (p *Pipeline) CleanScene(s ports.Scene) ports.Scene { return p.clean.scene(s) }

// Split answers or orders the requests of one output frame. Clean requests
// of a session other than the scene's, or with no session in the scene, are
// failed with ErrSessionInactive, those newer than the scene's Revision with
// ErrSessionStale (never served as normal captures), and removed. The rest is reordered in place: normal is the prefix, drawn from
// the displayed frame; clean is the following requests, drawn from CleanScene
// rendered first, or from the child scene when s.CaptureScene is set (see
// Offscreen). clean is empty when the clean frame would equal the displayed
// one, its requests then stay in normal. The entries past
// len(normal)+len(clean) are zeroed. No allocation.
func (p *Pipeline) Split(s ports.Scene, reqs []ports.CaptureRequest) (normal, clean []ports.CaptureRequest) {
	n := 0
	for _, q := range reqs {
		if q.Clean {
			var err error
			switch {
			case s.Capture == nil || q.Session != s.Capture.Session:
				err = ErrSessionInactive
			case s.Capture.Revision < q.CaptureRevision:
				err = ErrSessionStale
			}
			if err != nil {
				Fail(p.ctx, q, err, p.replies)
				continue
			}
		}
		reqs[n] = q
		n++
	}
	clear(reqs[n:])
	// A hidden workspace (CaptureScene) is always a separate frame.
	if s.CaptureScene == nil && !cleanDiffers(s) {
		return reqs[:n], nil
	}
	j := 0
	for i := range n {
		if !reqs[i].Clean {
			reqs[i], reqs[j] = reqs[j], reqs[i]
			j++
		}
	}
	return reqs[:j], reqs[j:n]
}

// Handed reports a zeroed request: one Split failed, or one already given to
// the worker. The owner never answers it again.
func Handed(q ports.CaptureRequest) bool { return q.ID == 0 && q.Dst.File == nil }
