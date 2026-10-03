package drm

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Tearing, variable refresh and content type of frame commits.

// probeAsync checks once whether async commits may carry IN_FENCE_FD. An
// inactive CRTC refuses async flips: the probe waits until it is on.
func (o *Output) probeAsync(r ports.Renderer) {
	if o.asyncProbed || !o.tearing || o.off {
		return
	}
	o.asyncProbed = true
	r.UseTarget(o.back)
	done, err := r.Render(ports.Scene{Background: "#000000"}, nil)
	if err != nil {
		return
	}
	req := &atomicReq{}
	req.set(o.primary.id, o.primary.prop("FB_ID"), uint64(o.fbs[o.back]))
	if done != nil {
		defer done.Close()
		req.set(o.primary.id, o.primary.prop("IN_FENCE_FD"), uint64(done.Fd()))
	}
	err = o.k.commit(req, atomicTestOnly|flipAsyncFlag, 0)
	o.asyncFence = err == nil && done != nil
	o.log.Info().Err(err).Str("connector", o.conn.name).Bool("async_fence", o.asyncFence).Msg("tearing probe")
}

// wantVRR is the VRR state the next frame commit sets: on while game (a
// buffer is scanned out or a fullscreen window covers the output), off
// after vrrHold without it (each toggle may flicker, so short breaks keep
// it).
func (o *Output) wantVRR(game bool) bool {
	if o.vrrProp == 0 {
		return false
	}
	o.vrrGame = game
	if game {
		o.composedSince = time.Time{}
		return true
	}
	if o.composedSince.IsZero() {
		o.composedSince = time.Now()
	}
	return o.vrrOn && time.Since(o.composedSince) <= vrrHold
}

// stateVRR is the VRR state of a commit without a new frame (cursor,
// vrrOff timer): the last frame's. A shown buffer is not enough: it may
// be a tiled window on the overlay plane.
func (o *Output) stateVRR() bool { return o.wantVRR(o.vrrGame) }

// contentProps changes the connector hint only when it differs from the applied value.
func (o *Output) contentProps(req *atomicReq) {
	if o.contentProp != 0 && o.contentWanted != o.contentValue {
		req.set(o.conn.id, o.contentProp, o.contentWanted)
	}
}

// wantContent chooses a content hint for the only visible fullscreen window.
func (o *Output) wantContent(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) {
	o.contentWanted = o.contentValues[0]
	var full *ports.SceneWindow
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.Hidden || w.Rect.W <= 0 || w.Rect.H <= 0 {
			continue
		}
		if !w.Fullscreen || !covers(&s, w) || full != nil {
			return
		}
		full = w
	}
	if full == nil {
		return
	}
	switch surfaces[full.ID].ContentType {
	case ports.ContentPhoto:
		o.contentWanted = o.contentValues[2]
	case ports.ContentVideo:
		o.contentWanted = o.contentValues[3]
	case ports.ContentGame:
		o.contentWanted = o.contentValues[4]
	}
}

// vrrHold is how long composition runs before VRR turns off.
const vrrHold = 500 * time.Millisecond

// setAsync logs when the output starts or stops tearing.
func (o *Output) setAsync(on bool) {
	if on != o.async {
		o.async = on
		o.log.Info().Bool("tearing", on).Str("connector", o.conn.name).Msg("tearing")
	}
}
