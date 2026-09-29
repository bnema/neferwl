package drm

import (
	"bytes"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestScaledScanoutRectAndCachedRefusal(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	o.cursor = nil
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	o.clientFBs[9] = &clientFB{fbID: 87}
	s := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}}}
	c := ports.SurfaceContent{ID: 1, Width: 100, Height: 50, LogicalW: 200, LogicalH: 100, Source: [4]float32{5, 2, 90, 46}, DMABuf: &ports.DMABuf{ID: 9, Format: fourccXRGB}}
	var log bytes.Buffer
	o.log = zerowrap.New(zerowrap.Config{Output: &log})
	surfaces := map[ports.WindowID]ports.SurfaceContent{1: c}
	if fb, _ := o.scanoutFrame(s, surfaces); fb != 0 || o.reason != "scale_refused" {
		t.Fatalf("fb %d reason %s", fb, o.reason)
	}
	if len(*commits) != 1 || (*commits)[0].flags != atomicTestOnly {
		t.Fatalf("test commits %+v", *commits)
	}
	if v, _ := (*commits)[0].req.value(tPrimary, planeProps["SRC_X"]); v != 5<<16 {
		t.Fatalf("source x: %d", v)
	}
	if v, _ := (*commits)[0].req.value(tPrimary, planeProps["SRC_W"]); v != 90<<16 {
		t.Fatalf("source width: %d", v)
	}
	if v, _ := (*commits)[0].req.value(tPrimary, planeProps["CRTC_W"]); v != 200 {
		t.Fatalf("destination width: %d", v)
	}
	if fb, _ := o.scanoutFrame(s, surfaces); fb != 0 || len(*commits) != 1 {
		t.Fatal("refused geometry retried")
	}
	if !bytes.Contains(log.Bytes(), []byte("scale_refused")) {
		t.Fatalf("refusal not logged: %s", log.String())
	}
}

func TestModesetInvalidatesScaleDecision(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor = nil
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	fb := &clientFB{fbID: 87}
	o.clientFBs[9] = fb
	s := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}}}
	c := ports.SurfaceContent{ID: 1, Width: 100, Height: 50, LogicalW: 200, LogicalH: 100, DMABuf: &ports.DMABuf{ID: 9, Format: fourccXRGB}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: c}
	if got, _ := o.scanoutFrame(s, contents); got != 87 || len(*commits) != 1 || !fb.scaleTestedOK {
		t.Fatalf("initial scale: %d, commits: %d", got, len(*commits))
	}
	k.EXPECT().createBlob(mock.Anything).Return(uint32(77), nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if fb.scaleTestedOK || fb.scaleRefused {
		t.Fatal("cached scale survived modeset")
	}
	if got, _ := o.scanoutFrame(s, contents); got != 87 || len(*commits) != 3 || (*commits)[2].flags != atomicTestOnly {
		t.Fatalf("scale not retested: fb %d commits %+v", got, *commits)
	}
}

func TestContentTypeFrameAndReset(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	o.contentProp = 81
	o.contentValues = [5]uint64{0, 1, 2, 3, 4}
	s := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}}}
	o.wantContent(s, map[ports.WindowID]ports.SurfaceContent{1: {ContentType: ports.ContentGame}})
	for i := 0; i < 2; i++ {
		if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok := (*commits)[0].req.value(tConn, 81); !ok || v != 4 {
		t.Fatalf("game hint: %d %v", v, ok)
	}
	if _, ok := (*commits)[1].req.value(tConn, 81); ok {
		t.Fatal("hint repeated")
	}
	o.wantContent(ports.Scene{}, nil)
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if v, ok := (*commits)[2].req.value(tConn, 81); !ok || v != 0 {
		t.Fatalf("reset hint: %d %v", v, ok)
	}
	o.contentProp = 0
	o.wantContent(s, map[ports.WindowID]ports.SurfaceContent{1: {ContentType: ports.ContentGame}})
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*commits)[3].req.value(tConn, 81); ok {
		t.Fatal("absent property set")
	}
}
