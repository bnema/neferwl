package capture

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func fenceScene(scale float64, w, h int, marks ...ports.CaptureIndicator) ports.Scene {
	return ports.Scene{Scale: scale, OutputWidth: w, OutputHeight: h, CaptureIndicators: marks}
}

func fenceRequest(id uint64, region image.Rectangle, since time.Time) ports.CaptureRequest {
	return ports.CaptureRequest{ID: id, Region: region, Indicate: true, Since: since}
}

// A request is served only from a scene that shows its mark: a border that
// contains the target on this output, or a pill for a workspace rendered for
// capture only. Requests that ask for none need none.
func TestIndicatorShown(t *testing.T) {
	whole := image.Rect(0, 0, 200, 100)
	border := func(r ports.Rect) ports.CaptureIndicator { return ports.CaptureIndicator{Rect: r} }
	pill := ports.CaptureIndicator{Pill: true, Rect: ports.Rect{X: 180, Y: 8, W: 12, H: 12}}
	for _, tc := range []struct {
		name  string
		scene ports.Scene
		req   ports.CaptureRequest
		want  bool
	}{
		{"no indicator", fenceScene(1, 200, 100), fenceRequest(1, whole, time.Time{}), false},
		{"request without a mark to show", fenceScene(1, 200, 100), ports.CaptureRequest{ID: 1, Region: whole}, true},
		{"whole output border", fenceScene(1, 200, 100, border(ports.Rect{W: 200, H: 100})), fenceRequest(1, whole, time.Time{}), true},
		{"pill is not a border", fenceScene(1, 200, 100, pill), fenceRequest(1, whole, time.Time{}), false},
		{"region border", fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 20, H: 20})), fenceRequest(1, image.Rect(10, 10, 30, 30), time.Time{}), true},
		{"border of another region", fenceScene(1, 200, 100, border(ports.Rect{X: 50, Y: 10, W: 20, H: 20})), fenceRequest(1, image.Rect(10, 10, 30, 30), time.Time{}), false},
		{"smaller border than the region", fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 10, H: 10})), fenceRequest(1, image.Rect(10, 10, 30, 30), time.Time{}), false},
		{"larger border contains the region", fenceScene(1, 200, 100, border(ports.Rect{W: 200, H: 100})), fenceRequest(1, image.Rect(10, 10, 30, 30), time.Time{}), true},
		{"scaled region rounds outwards", fenceScene(1.5, 200, 100, border(ports.Rect{X: 1, Y: 1, W: 3, H: 3})), fenceRequest(1, image.Rect(1, 1, 6, 6), time.Time{}), true},
		{"scaled region outside", fenceScene(1.5, 200, 100, border(ports.Rect{X: 1, Y: 1, W: 3, H: 3})), fenceRequest(1, image.Rect(1, 1, 7, 6), time.Time{}), false},
		{"edge at the output edge reaches it", fenceScene(1.5, 201, 100, border(ports.Rect{X: 0, Y: 0, W: 201, H: 100})), fenceRequest(1, image.Rect(0, 0, 302, 150), time.Time{}), true},
		{"undrawable thin border shows nothing", fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 1, H: 20})), fenceRequest(1, image.Rect(10, 10, 11, 30), time.Time{}), false},
		{"undrawable flat border shows nothing", fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 20, H: 1})), fenceRequest(1, image.Rect(10, 10, 30, 11), time.Time{}), false},
		{"smallest drawable border", fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 2, H: 2})), fenceRequest(1, image.Rect(10, 10, 12, 12), time.Time{}), true},
		{"pill cut by a short output shows nothing", fenceScene(1, 200, 8, ports.CaptureIndicator{Pill: true, Rect: ports.Rect{X: 180, Y: 8, W: 12, H: 12}}), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, false},
		{"pill partly outside shows nothing", fenceScene(1, 200, 100, ports.CaptureIndicator{Pill: true, Rect: ports.Rect{X: 195, Y: 8, W: 12, H: 12}}), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, false},
		{"whole-output pill of a tiny output", fenceScene(1, 200, 8, ports.CaptureIndicator{Pill: true, Rect: ports.Rect{W: 200, H: 8}}), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, true},
		{"empty pill shows nothing", fenceScene(1, 200, 100, ports.CaptureIndicator{Pill: true}), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, false},
		{"off-screen needs the pill", fenceScene(1, 200, 100, pill), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, true},
		{"off-screen border is not its mark", fenceScene(1, 200, 100, border(ports.Rect{W: 200, H: 100})), ports.CaptureRequest{ID: 1, Indicate: true, OffScreen: true, Workspace: 3, Region: whole}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IndicatorShown(tc.scene, tc.req))
		})
	}
}

// A request waits HoldFor for its indicator, then fails closed; one whose
// indicator shows is never failed. No allocation.
func TestExpireFailsOnlyTheRequestsWhoseIndicatorNeverCame(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	now := time.Unix(100, 0)
	s := fenceScene(1, 200, 100, ports.CaptureIndicator{Rect: ports.Rect{W: 200, H: 100}})
	shown := fenceRequest(1, image.Rect(0, 0, 200, 100), now.Add(-time.Hour))
	late := fenceRequest(2, image.Rect(0, 0, 200, 100), now.Add(-time.Hour))
	late.OffScreen = true
	young := fenceRequest(3, image.Rect(0, 0, 200, 100), now.Add(-HoldFor/2))
	young.OffScreen = true
	kept, wait := p.Expire(s, []ports.CaptureRequest{shown, late, young}, now)
	require.Equal(t, []uint64{1, 3}, ids(kept))
	require.Equal(t, HoldFor/2, wait)
	d := <-replies
	require.Equal(t, uint64(2), d.ID)
	require.ErrorIs(t, d.Err, ErrIndicatorMissing)
	// Nothing waits: no timer needed.
	_, wait = p.Expire(s, []ports.CaptureRequest{shown}, now)
	require.Zero(t, wait)

	reqs := []ports.CaptureRequest{young, shown}
	if n := testing.AllocsPerRun(100, func() { _, _ = p.Expire(s, reqs, now) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// Hold hands over the requests whose indicator the scene shows and keeps the
// others, in place.
func TestHoldSplitsByIndicator(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 4))
	defer p.Close(nil)
	now := time.Unix(100, 0)
	none := fenceScene(1, 200, 100)
	with := fenceScene(1, 200, 100, ports.CaptureIndicator{Rect: ports.Rect{W: 200, H: 100}})
	a := fenceRequest(1, image.Rect(0, 0, 200, 100), now)
	b := fenceRequest(2, image.Rect(0, 0, 200, 100), now)
	reqs := []ports.CaptureRequest{a, b}
	ready, held := p.Hold(none, reqs, now)
	require.Empty(t, ready)
	require.Equal(t, []uint64{1, 2}, ids(held))
	ready, held = p.Hold(with, reqs, now)
	require.Equal(t, []uint64{1, 2}, ids(ready))
	require.Empty(t, held)
	if n := testing.AllocsPerRun(100, func() { _, _ = p.Hold(with, reqs, now) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// Waiting keeps what was neither handed over nor shown.
func TestWaitingKeepsOnlyHeldRequests(t *testing.T) {
	now := time.Unix(100, 0)
	with := fenceScene(1, 200, 100, ports.CaptureIndicator{Rect: ports.Rect{W: 200, H: 100}})
	held := fenceRequest(3, image.Rect(0, 0, 200, 100), now)
	held.OffScreen = true
	reqs := []ports.CaptureRequest{{}, held, fenceRequest(4, image.Rect(0, 0, 200, 100), now)}
	got := Waiting(reqs, with)
	require.Equal(t, []uint64{3}, ids(got))
	require.Equal(t, 3, cap(reqs[:cap(got)]), "the owner's storage is kept")
	require.True(t, Handed(reqs[1]) && Handed(reqs[2]), "entries past the kept ones are zeroed")
}

// A scale or mode change while a request is held: the request keeps its
// physical region, the fence judges it against the scene that will draw it.
// Whatever the new scene draws, the region is served only when the drawn mark
// covers it in that scene's pixels; otherwise it stays held and fails after
// HoldFor. Nothing is served unindicated.
func TestHeldRequestAcrossScaleAndModeChange(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	defer p.Close(nil)
	now := time.Unix(100, 0)
	region := image.Rect(10, 10, 30, 30) // physical, asked at scale 1
	q := fenceRequest(1, region, now)
	border := func(r ports.Rect) ports.CaptureIndicator { return ports.CaptureIndicator{Rect: r} }
	old := fenceScene(1, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 20, H: 20}))
	require.True(t, IndicatorShown(old, q))

	for _, tc := range []struct {
		name  string
		scene ports.Scene
		shown bool
	}{
		{"scale doubled, mark of the old logical region", fenceScene(2, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 20, H: 20})), false},
		{"scale doubled, mark re-derived for the region", fenceScene(2, 200, 100, border(ports.Rect{X: 5, Y: 5, W: 10, H: 10})), true},
		{"scale halved, old mark too small in pixels", fenceScene(0.5, 200, 100, border(ports.Rect{X: 10, Y: 10, W: 20, H: 20})), false},
		{"mode change, mark of the new whole output", fenceScene(1, 400, 200, border(ports.Rect{W: 400, H: 200})), true},
		{"mode change, no mark yet", fenceScene(1, 400, 200), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.shown, IndicatorShown(tc.scene, q))
			reqs := []ports.CaptureRequest{q}
			ready, held := p.Hold(tc.scene, reqs, now)
			if tc.shown {
				require.Equal(t, []uint64{1}, ids(ready))
				return
			}
			require.Empty(t, ready)
			require.Equal(t, []uint64{1}, ids(held))
			// Still held before HoldFor; failed after, never served.
			kept, wait := p.Expire(tc.scene, held, now.Add(HoldFor/2))
			require.Len(t, kept, 1)
			require.Positive(t, wait)
			kept, _ = p.Expire(tc.scene, kept, now.Add(HoldFor))
			require.Empty(t, kept)
			d := <-replies
			require.ErrorIs(t, d.Err, ErrIndicatorMissing)
		})
	}
}
