package hints

import (
	"context"
	"hash/maphash"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestWindowCrop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     ports.Rect
		scale float64
		want  image.Rectangle
	}{
		{"unit", ports.Rect{X: 10, Y: 20, W: 100, H: 50}, 1, image.Rect(10, 20, 110, 70)},
		{"fractional", ports.Rect{X: 11, Y: 21, W: 100, H: 50}, 1.2, image.Rect(13, 25, 134, 86)},
		{"off output", ports.Rect{X: -10, Y: 20, W: 100, H: 100}, 1.2, image.Rect(0, 24, 108, 100)},
		{"outside", ports.Rect{X: 1000, Y: 20, W: 100, H: 100}, 1, image.Rectangle{}},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, windowCrop(tc.r, tc.scale, 200, 100)) })
	}
}

func sourceResult(ar atspiResult) *pendingSource {
	p := &pendingSource{done: make(chan struct{}), res: ar}
	close(p.done)
	return p
}

func TestHybridCacheAndMerge(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // Cached OCR must not need tesseract.
	buf := make([]byte, 200*100*4)
	wins := []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 100, H: 100}}, {ID: 2, Rect: ports.Rect{X: 100, W: 100, H: 100}}}
	req := ports.HintScanRequest{Output: "o", Width: 200, Height: 100, Scale: 1, Windows: wins}
	seed := maphash.MakeSeed()
	r := windowCrop(wins[1].Rect, 1, 200, 100)
	oldWorkers := ocrWorkers
	ocrWorkers = 1 // one band covers the crop
	t.Cleanup(func() { ocrWorkers = oldWorkers })
	ext := image.Rect(0, 0, 100, 100)
	entry := bandCache{hash: (&worker{cacheSeed: seed}).cropHash(buf, 800, r), ext: ext, words: []OCRWord{{Text: "example.org", Rect: image.Rect(10, 20, 90, 40)}}}
	button := ports.HintTarget{Kind: ports.HintButton, Rect: ports.Rect{X: 1, Y: 1, W: 10, H: 10}}
	link := ports.HintTarget{Kind: ports.HintLink, Text: true, Rect: ports.Rect{X: 110, Y: 20, W: 80, H: 20}}
	uncoveredButton := ports.HintTarget{Kind: ports.HintButton, Rect: ports.Rect{X: 101, Y: 1, W: 10, H: 10}}
	ar := atspiResult{targets: []ports.HintTarget{button, uncoveredButton}, covered: map[ports.WindowID]bool{1: true}}
	w := worker{ocr: ocrState{done: true, binary: "tesseract", lang: "eng"}, cacheSeed: seed, cache: map[bandKey]bandCache{{output: "o", window: 2, band: 0, bands: 1}: entry}}
	got := w.scan(context.Background(), req, frame{pix: buf, stride: 800}, 0, ports.HintScanResult{}, sourceResult(ar))
	require.Equal(t, []ports.HintTarget{button, uncoveredButton, link}, got.Targets)
	req.LinksOnly = true
	got = w.scan(context.Background(), req, frame{pix: buf, stride: 800}, 0, ports.HintScanResult{}, sourceResult(ar))
	require.Equal(t, []ports.HintTarget{link}, got.Targets)
	// Changing one pixel invalidates only the crop, never leaving stale links.
	buf[100*4]++
	got = w.scan(context.Background(), req, frame{pix: buf, stride: 800}, 0, ports.HintScanResult{}, sourceResult(ar))
	require.Empty(t, got.Targets)
}

func TestMissingTesseractKeepsAccessibilityButtons(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	w := worker{}
	button := ports.HintTarget{Kind: ports.HintButton, Rect: ports.Rect{W: 5, H: 5}}
	req := ports.HintScanRequest{Width: 20, Height: 20, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 20}}}}
	got := w.scan(context.Background(), req, frame{pix: make([]byte, 1600), stride: 80}, 0, ports.HintScanResult{}, sourceResult(atspiResult{targets: []ports.HintTarget{button}}))
	require.NoError(t, got.Err)
	require.Equal(t, []ports.HintTarget{button}, got.Targets)
}

func TestNoWindowsProducesNoPixelTargets(t *testing.T) {
	pix, width, height := loadBGRA(t)
	w := worker{}
	got := w.scan(context.Background(), ports.HintScanRequest{Width: width, Height: height}, frame{pix: pix, stride: width * 4}, 0, ports.HintScanResult{}, sourceResult(atspiResult{}))
	require.Empty(t, got.Targets)
}

func TestStripMappingAndPolarity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	strips := packLines(img, []image.Rectangle{image.Rect(20, 20, 160, 40), image.Rect(20, 80, 160, 112)}, 2, true)
	require.Len(t, strips, 2)
	for _, s := range strips {
		for _, off := range s.offsets {
			require.Equal(t, 1, off.factor)
			require.Equal(t, uint8(255), s.img.GrayAt(off.packed.Min.X, off.packed.Min.Y).Y)
		}
	}
	small := packLines(img, []image.Rectangle{image.Rect(20, 20, 100, 30)}, 1, true)
	require.Equal(t, 2, small[0].offsets[0].factor)
}

func TestCropGrayAndHash(t *testing.T) {
	buf := make([]byte, 4*4*4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			copy(buf[y*16+x*4:], []byte{0, 0, 255, 255}) // BGRA red
		}
	}
	g := cropGray(buf, 16, image.Rect(1, 1, 3, 3), make([]byte, 4))
	require.Equal(t, image.Rect(0, 0, 2, 2), g.Bounds())
	require.Equal(t, uint8(76), g.GrayAt(1, 1).Y)
	w := worker{cacheSeed: maphash.MakeSeed()}
	a := w.cropHash(buf, 16, image.Rect(1, 1, 3, 3))
	buf[0]++ // outside the crop
	require.Equal(t, a, w.cropHash(buf, 16, image.Rect(1, 1, 3, 3)))
	buf[16+4]++
	require.NotEqual(t, a, w.cropHash(buf, 16, image.Rect(1, 1, 3, 3)))
}

func TestUpscaleBandGray(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 3, 2))
	for i := range g.Pix {
		g.Pix[i] = uint8(i + 1)
	}
	sub := g.SubImage(image.Rect(1, 1, 3, 2)).(*image.Gray)
	up := upscaleBand(sub)
	require.Equal(t, image.Rect(0, 0, 4, 2), up.Bounds())
	require.Equal(t, []uint8{5, 5, 6, 6, 5, 5, 6, 6}, up.Pix)
}

// A blinking cursor changes one band: the other bands keep their cached
// words, and only the changed one is recognized again.
func TestBandCacheSurvivesCursorBlink(t *testing.T) {
	binary, err := exec.LookPath("tesseract")
	if err != nil {
		t.Skip("tesseract not on PATH")
	}
	old := ocrWorkers
	ocrWorkers = 4
	t.Cleanup(func() { ocrWorkers = old })
	f, err := os.Open("testdata/terminal.png")
	require.NoError(t, err)
	defer f.Close()
	src, err := png.Decode(f)
	require.NoError(t, err)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	buf := make([]byte, w*h*4)
	for i := 0; i < len(buf); i += 4 {
		buf[i], buf[i+1], buf[i+2], buf[i+3] = rgba.Pix[i+2], rgba.Pix[i+1], rgba.Pix[i], 0xff
	}
	_ = binary
	wk := &worker{}
	req := ports.HintScanRequest{Output: "o", Width: w, Height: h, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}}}
	first := wk.scan(context.Background(), req, frame{pix: buf, stride: w * 4}, 0, ports.HintScanResult{}, sourceResult(atspiResult{}))
	require.NotEmpty(t, first.Targets)
	require.Len(t, wk.cache, min(runtime.GOMAXPROCS(0), 4))

	// The same frame: every band hits.
	again := wk.scan(context.Background(), req, frame{pix: buf, stride: w * 4}, 0, ports.HintScanResult{}, sourceResult(atspiResult{}))
	require.Equal(t, first.Targets, again.Targets)

	// One pixel at the bottom blinks: only the last band (and the context of
	// none other) is invalidated.
	before := map[bandKey]uint64{}
	for k, v := range wk.cache {
		before[k] = v.hash
	}
	buf[((h-1)*w+3)*4] ^= 0xff
	blink := wk.scan(context.Background(), req, frame{pix: buf, stride: w * 4}, 0, ports.HintScanResult{}, sourceResult(atspiResult{}))
	require.Equal(t, first.Targets, blink.Targets)
	changed := 0
	for k, v := range wk.cache {
		if before[k] != v.hash {
			changed++
		}
	}
	require.Equal(t, 1, changed)
}
