package vulkan

import (
	"context"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

var sessionRed = color.RGBA{R: 0xff, G: 0x3b, B: 0x30, A: 255}

func sessionScene() ports.Scene {
	return ports.Scene{
		Seq: 1, Scale: 1, Background: "#101010",
		Capture: &ports.SceneCapture{
			Session: 7, TargetRect: ports.Rect{X: 8, Y: 4, W: 16, H: 8},
			BorderWidth: 2, BorderColor: ports.CaptureBorderColor, Excluded: []ports.WindowID{9},
		},
	}
}

func TestCaptureBorderGeometryLogicalScaled(t *testing.T) {
	for _, tc := range []struct {
		scale float64
		want  []image.Rectangle
	}{
		{1, []image.Rectangle{image.Rect(8, 4, 24, 6), image.Rect(8, 10, 24, 12), image.Rect(8, 6, 10, 10), image.Rect(22, 6, 24, 10)}},
		{2, []image.Rectangle{image.Rect(16, 8, 48, 12), image.Rect(16, 20, 48, 24), image.Rect(16, 12, 20, 20), image.Rect(44, 12, 48, 20)}},
	} {
		r := &Renderer{width: 64, height: 64}
		s := sessionScene()
		s.Scale = tc.scale
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64)))
		require.Len(t, ds, 4)
		for i, d := range ds {
			got := image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3]))
			require.Equal(t, tc.want[i], got, "scale %v side %d", tc.scale, i)
			require.Equal(t, [4]float32{0xff / 255.0, 0x3b / 255.0, 0x30 / 255.0, 1}, d.pc.color)
		}
	}
	// On the output edge the border is still inside the output.
	r := &Renderer{width: 64, height: 64}
	s := sessionScene()
	s.Scale = 1
	s.Capture.TargetRect = ports.Rect{W: 64, H: 64}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64)))
	require.Len(t, ds, 4)
	require.Equal(t, [4]int32{0, 0, 64, 2}, ds[0].pc.rect)
	require.Equal(t, [4]int32{62, 2, 64, 62}, ds[3].pc.rect)
	// A width larger than half the smaller side is clamped: no overdraw.
	s.Capture.TargetRect, s.Capture.BorderWidth = ports.Rect{X: 4, Y: 4, W: 10, H: 6}, 9
	ds = r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64)))
	require.Equal(t, [4]int32{4, 4, 14, 7}, ds[0].pc.rect)
	require.Equal(t, [4]int32{4, 7, 14, 10}, ds[1].pc.rect)
	require.Len(t, ds, 2) // the side fills would be empty
	// No session, or no border: nothing extra is drawn.
	s = sessionScene()
	s.Capture.BorderWidth = 0
	require.Empty(t, r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64))))
	s.Capture = nil
	require.Empty(t, r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64))))
}

func pixelAt(t *testing.T, r *Renderer, cf ports.CaptureFrame, x, y int) color.RGBA {
	t.Helper()
	dst := make([]byte, 4)
	require.NoError(t, cf.Read(image.Rect(x, y, x+1, y+1), dst, 4))
	return color.RGBA{dst[2], dst[1], dst[0], 255}
}

// The clean frame is rendered and copied first, then the displayed frame:
// the first copy has no border, the second one does, and the displayed frame
// (Pixels) matches the second.
func testCleanThenDisplayed(t *testing.T, r *Renderer, tol int, red color.RGBA) {
	s := sessionScene()
	p := capture.NewPipeline(context.Background(), nil)
	defer p.Close(nil)
	clean := p.CleanScene(s)
	require.Nil(t, clean.Capture)
	require.Zero(t, clean.Seq)
	require.NoError(t, render(r, clean, nil))
	cleanCopy := captureWait(t, r)
	defer r.EndCapture(cleanCopy)
	require.NoError(t, render(r, s, nil))
	shownCopy := captureWait(t, r)
	defer r.EndCapture(shownCopy)
	near := func(a, b color.RGBA) bool {
		d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }
		return max(d(a.R, b.R), d(a.G, b.G), d(a.B, b.B)) <= tol
	}
	bg := pixelAt(t, r, shownCopy, 1, 1)
	// (8, 8) and (23, 8) lie in the 2 px border on the target's edge, (12, 8) inside it.
	require.True(t, near(pixelAt(t, r, shownCopy, 8, 8), red), "displayed border")
	require.True(t, near(pixelAt(t, r, shownCopy, 23, 8), red), "displayed border")
	require.True(t, near(pixelAt(t, r, cleanCopy, 8, 8), bg), "clean copy has no border")
	require.True(t, near(pixelAt(t, r, cleanCopy, 23, 8), bg), "clean copy has no border")
	require.True(t, near(pixelAt(t, r, shownCopy, 7, 8), bg), "nothing outside the target")
	require.True(t, near(pixelAt(t, r, shownCopy, 12, 8), bg), "target interior untouched")
}

func TestCaptureCleanBeforeDisplayedSDR(t *testing.T) {
	r, err := New(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	testCleanThenDisplayed(t, r, 0, sessionRed)
}

func TestCaptureCleanBeforeDisplayedHDR(t *testing.T) {
	r := hdrTestRenderer(t)
	// The HDR capture pass tone-maps: the expectation is the same curve.
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= .04045 {
			return f / 12.92
		}
		return math.Pow((f+.055)/1.055, 2.4)
	}
	want := captureSDR([3]float64{lin(sessionRed.R), lin(sessionRed.G), lin(sessionRed.B)})
	testCleanThenDisplayed(t, r, 3, color.RGBA{want[0], want[1], want[2], 255})
}
