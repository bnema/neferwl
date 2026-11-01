package vulkan

import (
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func hdrTestRenderer(t *testing.T) *Renderer {
	return hdrTestRendererVariant(t, false)
}

func hdrTestRendererVariant(t *testing.T, single bool) *Renderer {
	t.Helper()
	r := newTestRenderer(t, 64, 16)
	t.Cleanup(r.Close)
	if single {
		r.destroyComposer()
		r.compose.resources, r.compose.indexing = 1, false
		if err := r.createComposer(); err != nil {
			t.Fatal(err)
		}
	}
	r.SetHDR(203)
	r.SetHDRReadback(true)
	if r.gpu.physical == 0 {
		t.Skip("no exportable GPU")
	}
	mods := r.gpu.hdrReadbackMods
	if len(r.gpu.hdrReadbackMods) == 0 {
		t.Skip("no HDR transfer-src modifier")
	}
	bufs, err := r.ExportTargets(1, mods, false)
	if err != nil {
		t.Skipf("no HDR target: %v", err)
	}
	t.Cleanup(func() { bufs[0].Planes[0].File.Close() })
	return r
}

// pqTargetWords reads a test-only PQ target back (production targets are
// not transfer-src), staging through a capture slot's buffer.
func pqTargetWords(t *testing.T, r *Renderer) []uint32 {
	t.Helper()
	var words []uint32
	if err := r.readHDRTarget(func(w []uint32) { words = slices.Clone(w) }); err != nil {
		t.Fatal(err)
	}
	return words
}

func hdrTargetAt(t *testing.T, r *Renderer, x, y int) [3]float64 {
	t.Helper()
	pixel := pqTargetWords(t, r)[y*r.width+x]
	return [3]float64{float64(pixel>>20&1023) / 1023, float64(pixel>>10&1023) / 1023, float64(pixel&1023) / 1023}
}

// hdrPQScene is a 64×16 XR30 udmabuf client holding 1000-nit neutral, red
// and blue BT.2020 thirds (x/20), drawn full-output.
func hdrPQScene(t *testing.T, r *Renderer) (domain.Scene, map[domain.WindowID]domain.SurfaceContent, [3][3]float64) {
	t.Helper()
	format := domain.DMABufFormat{Format: fourcc('X', 'R', '3', '0'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, format) {
		t.Skip("linear XR30 import unavailable")
	}
	nits := [3][3]float64{{1000, 1000, 1000}, {1000, 0, 0}, {0, 0, 1000}}
	f := udmabuf(t, 64, 16, func(x, _ int) [4]byte {
		v := nits[min(x/20, 2)]
		b, g, red := uint32(math.Round(pqEncode(v[2])*1023)), uint32(math.Round(pqEncode(v[1])*1023)), uint32(math.Round(pqEncode(v[0])*1023))
		p := b | g<<10 | red<<20
		return [4]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)}
	})
	buf := &domain.DMABuf{ID: 2020, Width: 64, Height: 16, Format: format.Format, Planes: []domain.DMABufPlane{{File: f, Stride: 256}}}
	c := domain.SurfaceContent{ID: 1, Seq: 1, Width: 64, Height: 16, LogicalW: 64, LogicalH: 16, Opaque: true, DMABuf: buf, Color: domain.SurfaceColor{TF: domain.ColorTFPQ, Primaries: domain.ColorPrimariesBT2020}}
	scene := domain.Scene{Seq: 1, Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
	return scene, map[domain.WindowID]domain.SurfaceContent{1: c}, nits
}

// HDRPixels returns the exported PQ target's codes scaled to 16 bits, and
// nothing on an SDR renderer.
func TestHDRPixels(t *testing.T) {
	r := hdrTestRenderer(t)
	if r.HDRPixels() != nil {
		t.Fatal("HDRPixels before any frame")
	}
	scene, contents, nits := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	img := r.HDRPixels()
	if img == nil {
		t.Fatal("HDRPixels nil after an HDR frame")
	}
	if img.Bounds() != image.Rect(0, 0, 64, 16) {
		t.Fatalf("bounds %v", img.Bounds())
	}
	for i, v := range nits {
		x := i*20 + 5
		got := img.RGBA64At(x, 8)
		raw := hdrTargetAt(t, r, x, 8) // code/1023, the same words
		for ch, c := range []uint16{got.R, got.G, got.B} {
			code := uint32(math.Round(raw[ch] * 1023))
			if want := uint16(code<<6 | code>>4); c != want {
				t.Errorf("sample %d channel %d: %#04x want %#04x", i, ch, c, want)
			}
			if d := math.Abs(float64(c)/65535 - pqEncode(v[ch])); d > .012 {
				t.Errorf("sample %d channel %d: %.4f want %.4f", i, ch, float64(c)/65535, pqEncode(v[ch]))
			}
		}
		if got.A != 0xffff {
			t.Errorf("sample %d alpha %#04x", i, got.A)
		}
	}
	// An HDR virtual output without SetHDRReadback has no readable target.
	plain := newTestRenderer(t, 64, 16)
	defer plain.Close()
	plain.SetHDR(203)
	plain.SetVirtualOutput(true)
	bufs, err := plain.ExportTargets(1, nil, false)
	if err != nil {
		t.Skipf("no HDR target: %v", err)
	}
	defer bufs[0].Planes[0].File.Close()
	if err := render(plain, domain.Scene{Background: "#ffffff"}, nil); err != nil {
		t.Fatal(err)
	}
	if plain.HDRPixels() != nil {
		t.Fatal("HDRPixels without SetHDRReadback")
	}
	sdr := newTestRenderer(t, 64, 4)
	defer sdr.Close()
	if err := render(sdr, domain.Scene{Background: "#ffffff"}, nil); err != nil {
		t.Fatal(err)
	}
	if sdr.HDRPixels() != nil {
		t.Fatal("HDRPixels on an SDR renderer")
	}
}

// HDRPixels reads the target the last HDR frame drew into, not the one
// selected for the next frame, and never a target exported after it.
func TestHDRPixelsReadsRenderedTarget(t *testing.T) {
	r := hdrTestRenderer(t)
	scene, contents, nits := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	// New targets replace the one the frame drew into: nothing to read.
	bufs, err := r.ExportTargets(2, r.gpu.hdrMods, false)
	if err != nil {
		t.Skipf("no two HDR targets: %v", err)
	}
	for _, b := range bufs {
		defer b.Planes[0].File.Close()
	}
	if r.HDRPixels() != nil {
		t.Fatal("HDRPixels from targets no frame drew into")
	}
	r.UseTarget(0)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	r.UseTarget(1)
	img := r.HDRPixels()
	if img == nil {
		t.Fatal("HDRPixels nil after an HDR frame")
	}
	for i, v := range nits {
		got := img.RGBA64At(i*20+5, 8)
		for ch, c := range []uint16{got.R, got.G, got.B} {
			if d := math.Abs(float64(c)/65535 - pqEncode(v[ch])); d > .012 {
				t.Errorf("sample %d channel %d: %.4f want %.4f", i, ch, float64(c)/65535, pqEncode(v[ch]))
			}
		}
	}
}

func TestHDRWindowedComposition(t *testing.T) {
	r := hdrTestRenderer(t)
	scene, contents, nits := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	for i, v := range nits {
		got := hdrTargetAt(t, r, i*20+5, 8)
		want := [3]float64{
			pqEncode(v[0]),
			pqEncode(v[1]),
			pqEncode(v[2]),
		}
		for ch := range got {
			if math.Abs(got[ch]-want[ch]) > .012 {
				t.Errorf("sample %d channel %d got %.4f want %.4f", i, ch, got[ch], want[ch])
			}
		}
	}
	neutral := hdrTargetAt(t, r, 5, 8)
	if math.Abs(neutral[0]-pqEncode(1000)) > .008 {
		t.Fatalf("1000 nits clipped: %v", neutral)
	}
	// SDR capture tone-maps: the 1000-nit white lands on the shoulder
	// (bright, not saturated) and the primaries keep their hue.
	want := captureSDR([3]float64{1000.0 / 203, 1000.0 / 203, 1000.0 / 203})
	if px := readPixels(t, r).RGBAAt(5, 8); !near(px, color.RGBA{want[0], want[1], want[2], 255}, 2) {
		t.Fatalf("tone-mapped capture pixel %v, want %v", px, want)
	}
	t.Run("async capture", func(t *testing.T) {
		cf := captureWait(t, r)
		defer r.EndCapture(cf)
		dst := make([]byte, 8)
		if err := cf.Read(image.Rect(5, 8, 7, 9), dst, 8); err != nil {
			t.Fatal(err)
		}
		if !near(color.RGBA{dst[2], dst[1], dst[0], 255}, color.RGBA{want[0], want[1], want[2], 255}, 2) || dst[3] != 255 {
			t.Fatalf("capture %v, want %v", dst, want)
		}
	})
	// The BT.2020 red (negative BT.709 green and blue) matches the
	// reference gamut reduction and shoulder, not a per-channel clip.
	red := readPixels(t, r).RGBAAt(25, 8)
	lin := [3]float64{1.660491, -0.124550, -0.018151}
	for i := range lin {
		lin[i] *= 1000.0 / 203
	}
	wantRed := captureSDR(lin)
	if !near(red, color.RGBA{wantRed[0], wantRed[1], wantRed[2], 255}, 3) || red.G != 0 {
		t.Fatalf("bright BT.2020 red captured as %v, want %v", red, wantRed)
	}
	before := r.redrawn
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= r.width*r.height {
		t.Fatal("unchanged HDR frame redrew full image")
	}
	if got := hdrTargetAt(t, r, 5, 8); math.Abs(got[0]-pqEncode(1000)) > .008 {
		t.Fatalf("partial redraw lost highlights: %v", got)
	}
}

func TestHDRLinearBlend(t *testing.T) {
	r := hdrTestRenderer(t)
	// Two translucent SDR overlays over black: half white then half black.
	white := shmContent(t, 64, 16, 64*4, fill(64, 16, [4]byte{128, 128, 128, 128}))
	black := shmContent(t, 64, 16, 64*4, fill(64, 16, [4]byte{0, 0, 0, 128}))
	scene := domain.Scene{Background: "#000000", Layers: []domain.SceneLayer{{ID: 1, Layer: domain.LayerOverlay, Rect: domain.Rect{W: 64, H: 16}}, {ID: 2, Layer: domain.LayerOverlay, Rect: domain.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: *white, 2: *black}); err != nil {
		t.Fatal(err)
	}
	got := hdrTargetAt(t, r, 5, 5)
	want := pqEncode(.25 * 203)
	for i, v := range got {
		if math.Abs(v-want) > .012 {
			t.Errorf("channel %d: got %.4f want %.4f", i, v, want)
		}
	}
}

func TestHDRSolidColorsMatchPartialRedraw(t *testing.T) {
	r := hdrTestRenderer(t)
	const background = "#804020"
	const border = "#4080c0"
	scene := domain.Scene{Seq: 1, Background: background, Border: domain.Border{Active: border}, Separators: []domain.Separator{{Rect: domain.Rect{X: 16, Y: 2, W: 2, H: 12}, Active: true}}, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{X: 20, Y: 2, W: 28, H: 12}}}}
	// The window body is a solid fill; the separator is another solid draw.
	content := solidContent(t, 28, 12, color.RGBA{128, 64, 32, 255})
	content.ID, content.Seq = 1, 1
	check := func(label string) {
		t.Helper()
		for _, point := range []struct {
			x, y int
			hex  string
		}{{2, 2, background}, {16, 4, border}, {24, 8, background}} {
			got := hdrTargetAt(t, r, point.x, point.y)
			col := parseColor(point.hex)
			var linear [3]float64
			for ch, b := range col {
				v := float64(b) / 255
				if v > .04045 {
					v = math.Pow((v+.055)/1.055, 2.4)
				} else {
					v /= 12.92
				}
				linear[ch] = v
			}
			matrix := [3][3]float64{{.627404, .329283, .043313}, {.069097, .919540, .011362}, {.016391, .088013, .895595}}
			for ch := range got {
				want := pqEncode((matrix[ch][0]*linear[0] + matrix[ch][1]*linear[1] + matrix[ch][2]*linear[2]) * 203)
				if math.Abs(got[ch]-want) > .012 {
					t.Errorf("%s (%d,%d) channel %d got %.4f want %.4f", label, point.x, point.y, ch, got[ch], want)
				}
			}
		}
	}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: content}); err != nil {
		t.Fatal(err)
	}
	check("full")
	before := r.redrawn
	// A changed window sequence damages only its rect; the background is
	// repainted there via a solid draw instead of the attachment clear.
	c := content
	c.Seq = 2
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= r.width*r.height {
		t.Fatal("expected partial redraw")
	}
	check("partial")
}

// Read back PQ codes and decode to linear SDR-white units. The tolerance
// includes fp16 composition and the exported target's 10-bit PQ quantization.
func TestHDRSDRExpansion(t *testing.T) {
	for _, single := range []bool{false, true} {
		name := "indexed"
		if single {
			name = "single"
		}
		t.Run(name, func(t *testing.T) { testHDRSDRExpansion(t, single) })
	}
}

func testHDRSDRExpansion(t *testing.T, single bool) {
	r := hdrTestRendererVariant(t, single)
	px := make([]byte, 64*16*4)
	for i := 0; i < 64*16; i++ {
		v := byte(i % 256)
		copy(px[i*4:], []byte{v, v, v, 255})
	}
	c := shmContent(t, 64, 16, 256, px)
	scene := domain.Scene{Seq: 1, Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
	contents := map[domain.WindowID]domain.SurfaceContent{1: *c}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	baseline := pqTargetWords(t, r)
	for _, curve := range []domain.SDRExpansionCurve{{}, (domain.SDRExpansion{PeakNits: 604, SDRWhiteNits: 203}).Curve()} {
		scene.Seq++
		scene.Windows[0].SDRExpansion = curve
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(baseline, pqTargetWords(t, r)) {
			t.Fatal("inactive curve changed output bits")
		}
	}
	for _, decode := range []domain.SDRDecode{domain.SDRDecodeGamma22, domain.SDRDecodeSRGB} {
		curve := (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203, Decode: decode}).Curve()
		scene.Seq++
		scene.Windows[0].SDRExpansion = curve
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
		words := pqTargetWords(t, r)
		prev := 0.0
		for i := 0; i < 256; i++ {
			got := pqDecode(float64(words[i]>>20&1023)/1023) / 203
			want := curve.Apply(domain.DecodeSDR(float64(i)/255, decode))
			if math.Abs(got-want) > .025 {
				t.Fatalf("%s level %d got %.6f mirror %.6f", decode, i, got, want)
			}
			if got < prev {
				t.Fatalf("%s ramp decreased at %d: %.6f < %.6f", decode, i, got, prev)
			}
			prev = got
			if i == 128 || i == 217 || i == 255 {
				t.Logf("%s encoded %.6f: readback %.6f mirror %.6f", decode, float64(i)/255, got, want)
			}
		}
	}
	// Even an active curve must not expand a popup.
	scene.Seq++
	scene.Windows[0].Popup = true
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(baseline, pqTargetWords(t, r)) {
		t.Fatal("popup expanded")
	}
}

func TestHDRSDRExpansionPremultipliedChild(t *testing.T) {
	r := hdrTestRenderer(t)
	// Straight RGB = (1, 0.5, 0.25), stored premultiplied at alpha 128/255.
	px := make([]byte, 64*16*4)
	for i := 0; i < 64*16; i++ {
		copy(px[4*i:], []byte{32, 64, 128, 128})
	}
	child := shmContent(t, 64, 16, 256, px)
	child.Surface, child.Version = 2, 1
	curve := (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203, Decode: domain.SDRDecodeSRGB}).Curve()
	scene := domain.Scene{Seq: 1, Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}, SDRExpansion: curve}}}
	root := domain.SurfaceContent{ID: 1, Width: 64, Height: 16, LogicalW: 64, LogicalH: 16, Children: []domain.Subsurface{{SurfaceContent: *child}}}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	a, b, c := curve.ApplyRGB(1, domain.DecodeSDR(.5, curve.Decode), domain.DecodeSDR(.25, curve.Decode))
	want := [3]float64{a * 128 / 255, b * 128 / 255, c * 128 / 255}
	codes := hdrTargetAt(t, r, 32, 8)
	// PQ target is BT.2020; invert its primary conversion after decoding.
	v := [3]float64{}
	for i := range v {
		v[i] = pqDecode(codes[i]) / 203
	}
	got := [3]float64{1.660491*v[0] - .587641*v[1] - .072850*v[2], -.124550*v[0] + 1.132900*v[1] - .008349*v[2], -.018151*v[0] - .100579*v[1] + 1.118730*v[2]}
	for i := range got {
		if math.Abs(got[i]-want[i]) > .025 {
			t.Fatalf("channel %d readback %.6f mirror %.6f", i, got[i], want[i])
		}
	}
	t.Logf("premultiplied child readback %v mirror %v", got, want)
}

func TestSDRTargetIgnoresExpansion(t *testing.T) {
	r := newTestRenderer(t, 64, 16)
	defer r.Close()
	c := solidContent(t, 64, 16, color.RGBA{240, 220, 200, 255})
	scene := domain.Scene{Seq: 1, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
	contents := map[domain.WindowID]domain.SurfaceContent{1: c}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	baseline := slices.Clone(readPixels(t, r).Pix)
	scene.Seq++
	scene.Windows[0].SDRExpansion = (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203}).Curve()
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(baseline, readPixels(t, r).Pix) {
		t.Fatal("SDR target changed")
	}
}

func TestHDRExpansionLeavesPQUnchanged(t *testing.T) {
	r := hdrTestRenderer(t)
	scene, contents, _ := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	baseline := pqTargetWords(t, r)
	scene.Seq++
	scene.Windows[0].SDRExpansion = (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203}).Curve()
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(baseline, pqTargetWords(t, r)) {
		t.Fatal("PQ surface changed")
	}
}

func TestExpansionParamsShaderLayout(t *testing.T) {
	curve := (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203, Decode: domain.SDRDecodeSRGB}).Curve()
	p := curve.Params()
	if p != [4]float32{float32(curve.Knee), float32(curve.Top), float32(curve.Shape), 1} {
		t.Fatalf("shader expects knee, top, shape, decode: %v", p)
	}
}
