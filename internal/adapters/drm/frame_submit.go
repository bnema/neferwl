package drm

import (
	"context"
	"time"

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
	presented := false // the frame was committed: what it gated is released
	if !o.sceneCurrent(scene) {
		return false, errSecurityScene
	}
	if o.protected {
		return false, o.submitProtectedFrame(ctx, r, scene, surfaces, seen)
	}
	// shows is what the frame puts on screen: presentation feedback of
	// windows it does not draw is discarded, not presented.
	f := pendingFrame{security: scene.Security, shows: o.shownBy(scene, seen)}
	o.wantContent(scene, surfaces)
	// Requests that cannot be served (exclusion of another session, a
	// workspace that moved) are failed here, never answered from the
	// displayed frame.
	// A request whose capture indicator the scene does not show yet is held
	// (the caller keeps it, see capture.Waiting).
	ready, _ := pipeline.Hold(scene, requests, time.Now())
	if len(scene.CaptureIndicators) > 0 {
		// The captures of this frame are delivered only once the frame that
		// carries their indicator is committed, whatever the path out (see
		// capture.Pipeline.EndGate): no failed or dropped frame serves one.
		pipeline.BeginGate()
		defer func() {
			if err == nil && !presented {
				err = capture.ErrFrameNotPresented // a panic on the way out
			}
			pipeline.EndGate(capture.GateVerdict(err))
		}()
	}
	normal, excluded, hidden := pipeline.Split(scene, ready)
	pipeline.Retire(scene)
	if len(hidden) > 0 {
		if !o.sceneCurrent(scene) || o.protected {
			return false, errSecurityScene
		}
		// A hidden workspace: the child renderer draws it, whatever the display
		// does (scanout included). Failures answer the requests and never stop
		// the output.
		pipeline.SubmitHidden(scene, surfaces, hidden)
		// Publish child holds before queuing a display flip, which may stall
		// past the stale-report timeout. Do not advance the display's Seen
		// until its own GPU work finishes.
		if o.capHidden != nil {
			o.report(nil, o.reports.Seen())
		}
		clear(hidden)
	}
	// Displayed-frame captures, Exclude requests and the capture indicator
	// force composition (scanout and the overlay plane would skip them); the
	// hidden-workspace child does not.
	decision := o.decideFrame(scene, surfaces, len(normal) > 0 || len(excluded) > 0 || len(scene.CaptureIndicators) > 0)
	if !o.sceneCurrent(scene) || o.protected {
		decision.overlay.close()
		return false, errSecurityScene
	}
	if decision.fb != 0 {
		c := decision.content
		if direct, err = o.commitScanoutRect(decision.fb, c, pendingFrame{security: scene.Security, shows: o.directShownBy(c.ID, seen[c.ID])}, decision.rect, decision.color); direct {
			presented = err == nil
			return true, err
		}
		decision = o.composeFrame(scene, surfaces)
	}
	ov, composed := decision.overlay, decision.composed
	r.UseTarget(o.back)
	// Captures never show the capture indicator: their frame goes first.
	took, pdone, perr := pipeline.SubmitPlain(r, composed, surfaces, normal)
	if perr != nil {
		o.holdRead(pdone)
		if pdone != nil {
			pdone.Close()
		}
		ov.close()
		return false, renderError{perr}
	}
	// Not capture admission: an engage during the render retains its client
	// reads; SubmitScoped already failed the requests of a stale epoch.
	if !o.sceneCurrent(scene) || o.protected {
		o.holdRead(pdone)
		if pdone != nil {
			pdone.Close()
		}
		ov.close()
		return false, errSecurityScene
	}
	if pdone != nil {
		pdone.Close()
	}
	if took {
		normal = nil // answered from the frame without the indicator
	}
	if len(excluded) > 0 {
		// The excluded frame goes first to the same target and is copied by
		// the capture; the displayed frame is drawn over it in queue order.
		// Cost: a second composition on frames with an excluded request.
		xdone, xerr := r.Render(pipeline.ExcludedScene(composed), surfaces)
		if xerr != nil {
			o.holdRead(xdone)
			if xdone != nil {
				xdone.Close()
			}
			ov.close()
			return false, renderError{xerr}
		}
		// An excluded composition is not capture admission. Engage may have
		// happened while Render submitted GPU work; retain its client reads
		// and leave the request credit with the caller on rejection.
		if !o.sceneCurrent(scene) || o.protected {
			o.holdRead(xdone)
			if xdone != nil {
				xdone.Close()
			}
			ov.close()
			return false, errSecurityScene
		}
		if xdone != nil {
			xdone.Close()
		}
		pipeline.SubmitScoped(scene.Security, r, excluded)
		// Ownership moved to the worker (or an immediate failure reply).
		clear(excluded)
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
	if !o.sceneCurrent(scene) || o.protected {
		o.holdRead(done)
		if done != nil {
			done.Close()
		}
		ov.close()
		return false, errSecurityScene
	}
	if len(normal) > 0 {
		// Scanout and the overlay plane were skipped for this frame.
		o.log.Debug().Str("connector", o.conn.name).Int("captures", len(normal)).Msg("capture frame composed")
		pipeline.SubmitScoped(scene.Security, r, normal)
		// Ownership moved to the worker (or an immediate failure reply).
		// A later commit panic must not fail the same requests again.
		clear(normal)
	}
	if !o.sceneCurrent(scene) || o.protected {
		o.holdRead(done)
		if done != nil {
			done.Close()
		}
		ov.close()
		return false, errSecurityScene
	}
	// The overlay buffer is on screen like a scanned-out one: it is
	// reported shown, so it is not released under the plane.
	f.queued, f.zeroCopy, f.composed = ov.buf, ov.id, true
	// A covering fullscreen window keeps VRR while composed (overlay
	// layer, subsurfaces, size mismatch).
	game := o.vrrProp != 0 && fullscreenShown(&scene)
	err = o.commitWith(o.fbs[o.back], done, false, o.wantVRR(game), f, ov)
	presented = err == nil
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
