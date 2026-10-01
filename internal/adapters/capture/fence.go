package capture

import (
	"errors"
	"image"
	"math"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The indicator fence. A capture is requested straight from wayland to the
// output owner, while core learns of it (CaptureFrameTaken) on its own
// channel and only then publishes a scene that carries the capture indicator.
// An output that served the request from the scene it already had would copy
// pixels before the border or pill was ever on screen. So the output owner
// holds every request until its current scene shows the mark the request
// needs, and serves it from that scene: the indicator is composed in the same
// frame as the capture or in an earlier one, never after it.
//
// Wayland stamps every capture it requests with Indicate. The mark needed is
// derived from the request alone:
//   - OffScreen (a workspace rendered for capture only): a pill;
//   - any other: a border whose rectangle covers the request's region.
//
// A mark that draws nothing (an empty rectangle, a border that would be 0
// wide, a pill not fully inside the output: ports.CaptureIndicator.DrawableIn) never counts as shown. Core
// inflates thin targets so their mark is drawable.
//
// Delivery is gated on the display (Pipeline.BeginGate, EndGate). The frame
// of a capture is rendered without the indicator and copied before the
// indicated frame, but its pixels are written and its reply sent only after
// the owner committed (DRM) or presented (headless) the frame that carries
// the indicator. When that frame fails to render or to commit, or its path
// ends in any other way, the capture fails with ErrIndicatorMissing (and the
// cause): a capture is never delivered for an indicator that was not shown.
// A scene without an indicator gates nothing and costs nothing.
//
// A request whose mark does not come (the target vanished, so core has
// nothing to mark) fails with ErrIndicatorMissing after HoldFor: nothing is
// ever served without its indicator. The fence costs nothing while no request
// is pending.

// HoldFor bounds how long a request waits for its indicator.
const HoldFor = 250 * time.Millisecond

// ErrIndicatorMissing fails a request whose indicator was not on screen in
// time.
var ErrIndicatorMissing = errors.New("capture indicator is not on screen")

// IndicatorShown reports whether scene s shows the indicator request q needs.
// A request that does not ask for one (Indicate false) needs none.
func IndicatorShown(s ports.Scene, q ports.CaptureRequest) bool {
	if !q.Indicate {
		return true
	}
	for _, m := range s.CaptureIndicators {
		if !m.DrawableIn(s.OutputWidth, s.OutputHeight) {
			// Defense in depth: a mark whose border would be empty shows
			// nothing, so it vouches for no capture.
			continue
		}
		if q.OffScreen {
			if m.Pill {
				return true
			}
			continue
		}
		if !m.Pill && covers(s, m.Rect, q.Region) {
			return true
		}
	}
	return false
}

// covers reports whether the border of logical rect m, drawn over a target
// region, covers the physical region r: m is projected to physical pixels the
// way wayland projects a logical target (rounded outwards). An edge of m on an
// output edge extends to it: the output size is not always a multiple of the
// scale.
func covers(s ports.Scene, m ports.Rect, r image.Rectangle) bool {
	scale := s.Scale
	if scale <= 0 {
		scale = 1
	}
	x0, y0 := int(math.Floor(float64(m.X)*scale)), int(math.Floor(float64(m.Y)*scale))
	x1, y1 := int(math.Ceil(float64(m.X+m.W)*scale)), int(math.Ceil(float64(m.Y+m.H)*scale))
	if m.X <= 0 {
		x0 = math.MinInt
	}
	if m.Y <= 0 {
		y0 = math.MinInt
	}
	if m.X+m.W >= s.OutputWidth {
		x1 = math.MaxInt
	}
	if m.Y+m.H >= s.OutputHeight {
		y1 = math.MaxInt
	}
	return r.Min.X >= x0 && r.Min.Y >= y0 && r.Max.X <= x1 && r.Max.Y <= y1
}

// expired reports whether q has waited for its indicator for HoldFor.
func expired(q ports.CaptureRequest, now time.Time) bool {
	return now.Sub(q.Since) >= HoldFor
}

// Expire fails the requests that waited HoldFor without their indicator
// being shown by s, and returns the others, in order, in place (the entries
// past them are zeroed). wait is how long until the next one expires, 0 when
// none waits for its indicator. No allocation.
func (p *Pipeline) Expire(s ports.Scene, reqs []ports.CaptureRequest, now time.Time) (kept []ports.CaptureRequest, wait time.Duration) {
	n := 0
	for _, q := range reqs {
		if !IndicatorShown(s, q) {
			if expired(q, now) {
				Fail(p.ctx, q, ErrIndicatorMissing, p.replies)
				continue
			}
			if d := HoldFor - now.Sub(q.Since); wait == 0 || d < wait {
				wait = d
			}
		}
		reqs[n] = q
		n++
	}
	clear(reqs[n:])
	return reqs[:n], wait
}

// Hold splits the requests of one output frame by the indicator of s: ready
// is the prefix whose indicator s shows (they go to Split), held the requests
// that keep waiting for a scene that shows theirs. Expired ones are failed
// first (see Expire). ready and held are consecutive in reqs; the entries
// past them are zeroed. No allocation.
func (p *Pipeline) Hold(s ports.Scene, reqs []ports.CaptureRequest, now time.Time) (ready, held []ports.CaptureRequest) {
	reqs, _ = p.Expire(s, reqs, now)
	j := 0
	for i := range reqs {
		if IndicatorShown(s, reqs[i]) {
			reqs[i], reqs[j] = reqs[j], reqs[i]
			j++
		}
	}
	return reqs[:j], reqs[j:]
}

// Waiting keeps, in place, the requests of a frame that still wait for the
// indicator of s: the ones neither handed over nor failed, whose indicator s
// does not show. The entries past them are zeroed. The capacity is kept, so
// the result replaces the owner's request list.
func Waiting(reqs []ports.CaptureRequest, s ports.Scene) []ports.CaptureRequest {
	n := 0
	for _, q := range reqs {
		if !Handed(q) && !IndicatorShown(s, q) {
			reqs[n] = q
			n++
		}
	}
	clear(reqs[n:])
	return reqs[:n]
}
