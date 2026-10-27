package capture

import (
	"fmt"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A request with Exclude leaves out the exclusion's HUD layers and popups;
// every other capture shows them. No capture shows the capture indicator
// (Scene.CaptureIndicators) or the position hint. Overlays (Scene.Overlays) are
// shown to the captures of Wayland clients only when they are marked Capture,
// never to the compositor's own captures (CaptureIDInternal). A scene that
// carries something a capture must not show is never the source of its image:
// requests of the displayed frame are served from a frame rendered without
// it first (SubmitPlain, ExcludedScene).
//
// A frame needs at most two batches: one for the displayed-frame requests
// (served from the displayed or from the plain frame, never both) and one
// for the Exclude requests. While overlays are kept for clients, an internal
// capture would need a third, overlay-free one: Split fails it instead.

// ErrInternalLabelled fails an internal capture (CaptureIDInternal) of a frame
// that has overlays kept for client captures (Overlay.Capture): serving it
// without overlays would need a third capture batch, and serving it with them
// would make the hint scanner read its own overlays. Core only starts a scan
// from the idle hint mode (core/hint.go hintRequest), so overlays and a scan in
// flight coincide only on a stale scene; the scan fails with a transient
// error and the user retries.
var ErrInternalLabelled = fmt.Errorf("%w: internal capture on a frame that keeps overlays for clients", ports.ErrCaptureTransient)

// ErrInternalExclude fails an internal Exclude capture of a frame that keeps
// overlays: its excluded frame carries them too.
var ErrInternalExclude = fmt.Errorf("%w: internal capture cannot read the excluded frame, which keeps overlays", ports.ErrCaptureTransient)

// ErrSessionInactive fails an Exclude request whose session is not the one
// the scene carries. Serving it as a normal capture would leak its HUD.
var ErrSessionInactive = fmt.Errorf("%w: capture exclusion is not active", ports.ErrCaptureTransient)

// ErrSessionStale fails an Exclude request stamped with a revision newer than
// the scene's: the scene predates a change of what an excluded frame must
// hide (a new HUD layer, popup, target), so its exclusion list could leak.
var ErrSessionStale = fmt.Errorf("%w: capture exclusion scene is older than the request", ports.ErrCaptureTransient)

// ErrWorkspaceMoved fails a workspace request whose workspace is neither on
// screen nor the one rendered off screen in the scene, or a window request
// whose window is not the one rendered off screen: it moved, went away, or
// the scene predates the session.
var ErrWorkspaceMoved = fmt.Errorf("%w: captured workspace is not where the request expected", ports.ErrCaptureTransient)

// excluder derives the scene of Exclude captures: the frame without the
// excluded layers and popups. Its scratch slices are reused,
// so a warmed output allocates nothing.
type excluder struct {
	windows  []domain.SceneWindow
	layers   []domain.SceneLayer
	overlays []domain.Overlay
}

// Shows reports whether a content change of id needs a new frame: the
// displayed scene draws it, or the hidden workspace of a capture does (its
// child render and the reads it releases). Flips and Shown still count the
// displayed scene only.
func Shows(s domain.Scene, id domain.WindowID) bool {
	return s.Shows(id) || s.CaptureScene != nil && s.CaptureScene.Shows(id)
}

// excludedShown reports whether the scene draws an excluded surface.
func excludedShown(s domain.Scene) bool {
	c := s.Capture
	if c == nil || len(c.Excluded) == 0 {
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

// keepsOverlays reports whether the scene has overlays that client captures
// keep (Overlay.Capture): the frames derived for captures keep them.
func keepsOverlays(s domain.Scene) bool {
	for _, o := range s.Overlays {
		if o.Capture {
			return true
		}
	}
	return false
}

// hidesOverlays reports whether the scene has overlays that client captures
// must not show (those without Overlay.Capture).
func hidesOverlays(s domain.Scene) bool {
	for _, o := range s.Overlays {
		if !o.Capture {
			return true
		}
	}
	return false
}

// internal reports a capture requested by the compositor itself (hint scan).
func internal(q domain.CaptureRequest) bool { return q.ID&ports.CaptureIDInternal != 0 }

// plain returns s as a capture shows it: without its capture state,
// indicators and position hint, and with only the overlays kept for clients
// (Overlay.Capture) in a new slice, never the caller's backing array. Seq is 0
// so the renderer redraws the whole target instead of reusing a displayed
// frame.
func plain(s domain.Scene) domain.Scene { return plainInto(s, nil) }

// plainInto is plain with the kept overlays appended to buf[:0], which must
// not alias s.Overlays.
func plainInto(s domain.Scene, buf []domain.Overlay) domain.Scene {
	s.Capture, s.CaptureIndicators, s.Seq = nil, nil, 0
	s.Overlays = captured(buf, s.Overlays)
	s.PositionHint = domain.PositionHint{}
	return s
}

// captured returns the overlays with Capture, appended to buf[:0]; nil when
// none.
func captured(buf, overlays []domain.Overlay) []domain.Overlay {
	kept := buf[:0]
	for _, o := range overlays {
		if o.Capture {
			kept = append(kept, o)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// SubmitPlain serves the displayed-frame requests of a scene that carries
// what captures must not show: the capture indicator, the position hint, or
// overlays that are not kept for clients. The frame is rendered
// without it, first (same renderer, queue order, like ExcludedScene), and
// copied before the displayed one is drawn. It reports whether it took the
// requests (and zeroed them); false when the displayed frame already shows
// what they may (no indicator, no position hint, and no overlays or overlays
// all kept for clients) or there is nothing to serve, then the caller submits
// them after the displayed frame as usual. On a render error the caller still
// owns the requests. Internal requests never reach it on a frame that keeps
// overlays: Split fails them.
//
// Admission is the session gate's: the requests are handed over with
// SubmitScoped under the scene's epoch, so a locked or changed epoch fails
// them before BeginCapture. The render fence is returned, never closed here:
// the plain render reads client buffers even when the requests are rejected,
// so the caller owns it (hold or wait it) whatever happened, error included.
func (p *Pipeline) SubmitPlain(r ports.Renderer, s domain.Scene, surfaces map[domain.WindowID]domain.SurfaceContent, normal []domain.CaptureRequest) (took bool, done *os.File, err error) {
	if len(normal) == 0 || len(s.CaptureIndicators) == 0 && !hidesOverlays(s) && !s.PositionHint.Shown() {
		return false, nil, nil
	}
	done, err = r.Render(plain(s), surfaces)
	if err != nil {
		return false, done, err
	}
	p.SubmitScoped(s.Security, r, normal)
	clear(normal)
	return true, done, nil
}

// scene returns s without the excluded windows
// (popups included) and layers. The result is valid until the next call.
func (c *excluder) scene(s domain.Scene) domain.Scene {
	var ex []domain.WindowID
	if s.Capture != nil {
		ex = s.Capture.Excluded
	}
	s = plainInto(s, c.overlays)
	if s.Overlays != nil {
		c.overlays = s.Overlays[:0]
	}
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

// ExcludedScene is the scene of Exclude captures: no excluded surface; see Split. Valid until the next call.
func (p *Pipeline) ExcludedScene(s domain.Scene) domain.Scene { return p.excl.scene(s) }

// Split answers or orders the requests of one output frame, in place. No
// allocation. Requests that cannot be served are failed and removed:
//   - internal requests of a frame that keeps overlays for clients
//     (ErrInternalLabelled, ErrInternalExclude);
//   - Exclude requests of a session other than the scene's, or with no
//     exclusion in it (ErrSessionInactive), and those newer than its Revision
//     (ErrSessionStale);
//   - workspace requests whose workspace is not where they expected
//     (ErrWorkspaceMoved).
//
// The rest is reordered:
//   - normal is the prefix, drawn from the displayed frame (or from the plain
//     frame, see SubmitPlain);
//   - excluded follows: Exclude requests, drawn from ExcludedScene rendered
//     first, or from the displayed frame (they stay in normal) when it shows
//     no excluded surface, no capture indicator or position hint, and no
//     overlays that must stay out of captures;
//   - hidden: the requests of the workspace rendered off screen
//     (OffScreen), served from CaptureScene (see SubmitHidden); they come last.
//
// The entries past the groups are zeroed.
func (p *Pipeline) Split(s domain.Scene, reqs []domain.CaptureRequest) (normal, excluded, hidden []domain.CaptureRequest) {
	n := 0
	for _, q := range reqs {
		var err error
		switch {
		case internal(q) && keepsOverlays(s):
			err = ErrInternalLabelled
			if q.Exclude {
				err = ErrInternalExclude
			}
		case q.Exclude && (s.Capture == nil || s.Capture.Session == 0 || q.Session != s.Capture.Session):
			err = ErrSessionInactive
		case q.Exclude && s.Capture.Revision < q.CaptureRevision:
			err = ErrSessionStale
		case q.Workspace != 0 && (s.Capture == nil || q.OffScreen && q.Workspace != s.Capture.Workspace || !q.OffScreen && q.Workspace != s.Capture.Shown):
			err = ErrWorkspaceMoved
		case q.Window != 0 && (s.Capture == nil || !q.OffScreen || q.Window != s.Capture.Window):
			err = ErrWorkspaceMoved
		}
		if err != nil {
			Fail(p.ctx, q, err, p.replies)
			continue
		}
		reqs[n] = q
		n++
	}
	clear(reqs[n:])
	reqs = reqs[:n]
	excl := excludedShown(s) || len(s.CaptureIndicators) > 0 || hidesOverlays(s) || s.PositionHint.Shown()
	group := func(q domain.CaptureRequest) int {
		switch {
		case q.OffScreen:
			return 2
		case q.Exclude && excl:
			return 1
		}
		return 0
	}
	var counts [3]int
	for _, q := range reqs {
		counts[group(q)]++
	}
	j := 0
	for g := range 3 {
		for i := j; i < n; i++ {
			if group(reqs[i]) == g {
				reqs[i], reqs[j] = reqs[j], reqs[i]
				j++
			}
		}
	}
	a, b := counts[0], counts[0]+counts[1]
	return reqs[:a], reqs[a:b], reqs[b:n]
}

// Handed reports a zeroed request: one Split failed, or one already given to
// the worker. The owner never answers it again.
func Handed(q domain.CaptureRequest) bool { return q.ID == 0 && q.Dst.File == nil && q.Mapped == nil }
