package drm

import (
	"context"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/ports"
)

// renderError is a frame the renderer failed to draw: the output stops.
type renderError struct{ err error }

func (e renderError) Error() string { return "render frame: " + e.err.Error() }
func (e renderError) Unwrap() error { return e.err }

// submitFrame puts one frame of the scene on screen: the fullscreen window
// scanned out directly when the kernel takes it, else a composed frame with
// an optional overlay plane, dropped when the kernel refuses it. Captures
// force composition and are answered from the composed frame; on renderError
// the caller still owns the requests and must fail them. direct reports scanout.
//
// On success the frame is in flight; errOverlayDropped asks for a new frame
// without the overlay; a renderError stops the output; other errors are
// commit errors for commitFailed.
func (o *Output) submitFrame(ctx context.Context, r ports.Renderer, scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, seen map[ports.WindowID]uint64, requests []ports.CaptureRequest, pipeline *capture.Pipeline) (direct bool, err error) {
	// shows is what the frame puts on screen: presentation feedback of
	// windows it does not draw is discarded, not presented.
	f := pendingFrame{shows: o.shownBy(scene, seen)}
	o.wantContent(scene, surfaces)
	// Clean requests that cannot be served (their session is not the
	// scene's) are failed here, never answered from the displayed frame.
	normal, clean := pipeline.Split(scene, requests)
	pipeline.Retire(scene)
	hiddenClean := len(clean) > 0 && scene.CaptureScene != nil
	if hiddenClean {
		// A hidden workspace: the child renderer draws it, whatever the display
		// does (scanout included). Failures answer the requests and never stop
		// the output.
		pipeline.SubmitHidden(scene, surfaces, clean)
		clear(clean)
		clean = nil
	}
	// Captures of the displayed frame, and a visible native session border,
	// force composition: scanout and the overlay plane would skip them. The
	// hidden-workspace child needs none, so it keeps direct scanout.
	decision := o.decideFrame(scene, surfaces, len(normal) > 0 || len(clean) > 0 || capture.BorderVisible(scene))
	if decision.fb != 0 {
		c := decision.content
		if direct, err = o.commitScanoutRect(decision.fb, c, pendingFrame{shows: o.directShownBy(c.ID, seen[c.ID])}, decision.rect); direct {
			return true, err
		}
		decision = o.composeFrame(scene, surfaces)
	}
	ov, composed := decision.overlay, decision.composed
	r.UseTarget(o.back)
	if len(clean) > 0 {
		// The clean frame goes first to the same target and is copied by
		// the capture; the displayed frame is drawn over it in queue order.
		// Cost: a second composition on frames with a clean request.
		cdone, cerr := r.Render(pipeline.CleanScene(composed), surfaces)
		if cerr != nil {
			return false, renderError{cerr}
		}
		if cdone != nil {
			cdone.Close()
		}
		pipeline.Submit(r, clean)
		// Ownership moved to the worker (or an immediate failure reply).
		clear(clean)
	}
	done, rerr := r.Render(composed, surfaces)
	if rerr != nil {
		ov.close()
		return false, renderError{rerr}
	}
	if ov.fb != 0 && !o.testOverlay(o.fbs[o.back], ov) {
		// Refused: compose the window too (cached per buffer).
		if done != nil {
			done.Close()
		}
		ov.close()
		ov = overlayWin{}
		if done, rerr = r.Render(scene, surfaces); rerr != nil {
			return false, renderError{rerr}
		}
	}
	if len(normal) > 0 {
		// Scanout and the overlay plane were skipped for this frame.
		o.log.Debug().Str("connector", o.conn.name).Int("captures", len(normal)).Msg("capture frame composed")
		pipeline.Submit(r, normal)
		// Ownership moved to the worker (or an immediate failure reply).
		// A later commit panic must not fail the same requests again.
		clear(normal)
	}
	// The overlay buffer is on screen like a scanned-out one: it is
	// reported shown, so it is not released under the plane.
	f.queued, f.zeroCopy, f.composed = ov.buf, ov.id, true
	// A covering fullscreen window keeps VRR while composed (overlay
	// layer, subsurfaces, size mismatch).
	game := o.vrrProp != 0 && fullscreenShown(&scene)
	err = o.commitWith(o.fbs[o.back], done, false, o.wantVRR(game), f, ov)
	if err != nil {
		// The GPU may still read client buffers for this frame.
		o.holdRead(done)
	}
	if done != nil {
		done.Close()
	}
	ov.close()
	if err != nil && o.overlayConflict(err, ov.buf) {
		return false, errOverlayDropped
	}
	if err == nil {
		o.queued = ov.buf
		o.back = 1 - o.back
	}
	return false, err
}
