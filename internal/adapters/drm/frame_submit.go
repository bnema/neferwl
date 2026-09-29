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
// force composition and are answered from the composed frame (they are
// consumed either way). direct reports a scanned-out frame.
//
// On success the frame is in flight; errOverlayDropped asks for a new frame
// without the overlay; a renderError stops the output; other errors are
// commit errors for commitFailed.
func (o *Output) submitFrame(ctx context.Context, r ports.Renderer, scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, seen map[ports.WindowID]uint64, requests []ports.CaptureRequest, captured chan<- ports.CaptureDone) (direct bool, err error) {
	// shows is what the frame puts on screen: presentation feedback of
	// windows it does not draw is discarded, not presented.
	f := pendingFrame{shows: o.shownBy(scene, seen)}
	decision := o.decideFrame(scene, surfaces, len(requests) > 0)
	if decision.fb != 0 {
		c := decision.content
		if direct, err = o.commitScanout(decision.fb, c, pendingFrame{shows: o.directShownBy(c.ID, seen[c.ID])}); direct {
			return true, err
		}
		decision = o.composeFrame(scene, surfaces)
	}
	ov, composed := decision.overlay, decision.composed
	r.UseTarget(o.back)
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
	if len(requests) > 0 {
		// Scanout and the overlay plane were skipped for this frame.
		o.log.Debug().Str("connector", o.conn.name).Int("captures", len(requests)).Msg("capture frame composed")
	}
	for _, q := range requests {
		capture.Write(ctx, q, r, captured)
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
