package capture

import (
	"errors"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// A request with Exclude leaves out the exclusion's HUD layers and popups;
// every other capture shows them. No capture shows the capture indicator
// (Scene.CaptureIndicators): a scene that carries it is never the source of a
// capture image. Requests of the displayed frame are then served from the
// frame rendered without it first (SubmitPlain, ExcludedScene).

// ErrSessionInactive fails an Exclude request whose session is not the one
// the scene carries. Serving it as a normal capture would leak its HUD.
var ErrSessionInactive = errors.New("capture exclusion is not active")

// ErrSessionStale fails an Exclude request stamped with a revision newer than
// the scene's: the scene predates a change of what an excluded frame must
// hide (a new HUD layer, popup, target), so its exclusion list could leak.
var ErrSessionStale = errors.New("capture exclusion scene is older than the request")

// ErrWorkspaceMoved fails a workspace request whose workspace is neither on
// screen nor the one rendered off screen in the scene: it moved, went away,
// or the scene predates the session.
var ErrWorkspaceMoved = errors.New("captured workspace is not where the request expected")

// excluder derives the scene of Exclude captures: the frame without the
// excluded layers and popups. Its scratch slices are reused,
// so a warmed output allocates nothing.
type excluder struct {
	windows []ports.SceneWindow
	layers  []ports.SceneLayer
}

// Shows reports whether a content change of id needs a new frame: the
// displayed scene draws it, or the hidden workspace of a capture does (its
// child render and the reads it releases). Flips and Shown still count the
// displayed scene only.
func Shows(s ports.Scene, id ports.WindowID) bool {
	return s.Shows(id) || s.CaptureScene != nil && s.CaptureScene.Shows(id)
}

// excludedShown reports whether the scene draws an excluded surface.
func excludedShown(s ports.Scene) bool {
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

// plain returns s without its capture state and indicators, Seq 0 so the
// renderer redraws the whole target instead of reusing a displayed frame.
func plain(s ports.Scene) ports.Scene {
	s.Capture, s.CaptureIndicators, s.Seq = nil, nil, 0
	return s
}

// SubmitPlain serves the displayed-frame requests of a scene that carries the
// capture indicator: the frame is rendered without it, first (same renderer,
// queue order, like ExcludedScene), and copied before the displayed one is
// drawn. It reports whether it took the requests (and zeroed them); false
// when the scene has no indicator or there is nothing to serve, the caller
// submits them after the displayed frame as usual. On a render error the
// caller still owns the requests.
//
// Admission is the session gate's: the requests are handed over with
// SubmitScoped under the scene's epoch, so a locked or changed epoch fails
// them before BeginCapture. The render fence is returned, never closed here:
// the plain render reads client buffers even when the requests are rejected,
// so the caller owns it (hold or wait it) whatever happened, error included.
func (p *Pipeline) SubmitPlain(r ports.Renderer, s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, normal []ports.CaptureRequest) (took bool, done *os.File, err error) {
	if len(normal) == 0 || len(s.CaptureIndicators) == 0 {
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
func (c *excluder) scene(s ports.Scene) ports.Scene {
	var ex []ports.WindowID
	if s.Capture != nil {
		ex = s.Capture.Excluded
	}
	s = plain(s)
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
func (p *Pipeline) ExcludedScene(s ports.Scene) ports.Scene { return p.excl.scene(s) }

// Split answers or orders the requests of one output frame, in place. No
// allocation. Requests that cannot be served are failed and removed:
// Exclude requests of a session other than the scene's, or with no exclusion
// in it (ErrSessionInactive), and those newer than its Revision
// (ErrSessionStale); workspace requests whose workspace is not where they
// expected (ErrWorkspaceMoved). The rest is reordered:
//   - normal is the prefix, drawn from the displayed frame;
//   - excluded follows: Exclude requests, drawn from ExcludedScene rendered first,
//     or from the displayed frame (they stay in normal) when it shows no
//     excluded surface and no capture indicator;
//   - hidden: the requests of the workspace rendered off screen
//     (OffScreen), served from CaptureScene (see SubmitHidden); they come last.
//
// The entries past the groups are zeroed.
func (p *Pipeline) Split(s ports.Scene, reqs []ports.CaptureRequest) (normal, excluded, hidden []ports.CaptureRequest) {
	n := 0
	for _, q := range reqs {
		var err error
		switch {
		case q.Exclude && (s.Capture == nil || s.Capture.Session == 0 || q.Session != s.Capture.Session):
			err = ErrSessionInactive
		case q.Exclude && s.Capture.Revision < q.CaptureRevision:
			err = ErrSessionStale
		case q.Workspace != 0 && (s.Capture == nil || q.OffScreen && q.Workspace != s.Capture.Workspace || !q.OffScreen && q.Workspace != s.Capture.Shown):
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
	excl := excludedShown(s) || len(s.CaptureIndicators) > 0
	group := func(q ports.CaptureRequest) int {
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
func Handed(q ports.CaptureRequest) bool { return q.ID == 0 && q.Dst.File == nil }
