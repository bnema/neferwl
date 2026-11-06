package vulkan

import (
	"image/color"
	"math"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
)

// Each plane can be backed by a separate DMA-BUF, as with VAAPI exports.
func yuvTestBuffer(t *testing.T, p010 bool, y, u, v int, rows ...[3]int) *domain.DMABuf {
	t.Helper()
	format := fourcc('N', 'V', '1', '2')
	if p010 {
		format = fourcc('P', '0', '1', '0')
	}
	planes := make([]domain.DMABufPlane, 2)
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("udmabuf unavailable: %v", err)
	}
	defer func() { _ = dev.Close() }()
	// Use a stride supported by the modifier on the device; the test
	// populates the exact client-provided row pitch.
	stride := uint32(256)

	ySize := int(stride) * 16
	uvSize := int(stride) * 8
	pages := (ySize + uvSize + os.Getpagesize() - 1) / os.Getpagesize() * os.Getpagesize()
	mem, err := unix.MemfdCreate("yuv-shared", unix.MFD_ALLOW_SEALING)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(mem) }()
	if err := unix.Ftruncate(mem, int64(pages)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, pages)
	for i := 0; i < 2; i++ {
		start, end := 0, ySize
		values := []int{y}
		if i == 1 {
			start, end, values = ySize, ySize+uvSize, []int{u, v}
		}
		for n, ch := start, 0; n < end; ch++ {
			val := values[ch%len(values)]
			if len(rows) > 0 {
				if i == 0 && (n-start)/int(stride) < 4 {
					val = rows[0][0]
				} else if i == 1 && (n-start)/int(stride) < 2 {
					val = rows[0][ch%len(values)+1]
				}
			}
			if p010 {
				code := uint16(val << 6)
				data[n], data[n+1] = byte(code), byte(code>>8)
				n += 2
			} else {
				data[n] = byte(val)
				n++
			}
		}
	}
	if _, err := unix.Pwrite(mem, data, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		t.Fatal(err)
	}
	arg := struct {
		memfd, flags uint32
		offset, size uint64
	}{memfd: uint32(mem), flags: 1, size: uint64(pages)}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), 0x40187542, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		t.Skipf("udmabuf create: %v", errno)
	}
	f := os.NewFile(fd, "yuv-shared")
	t.Cleanup(func() { _ = f.Close() })
	dup, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	f2 := os.NewFile(uintptr(dup), "yuv-shared-dup")
	t.Cleanup(func() { _ = f2.Close() })
	planes[0] = domain.DMABufPlane{File: f, Stride: stride}
	planes[1] = domain.DMABufPlane{File: f2, Offset: uint32(ySize), Stride: stride}
	return &domain.DMABuf{ID: uint64(format) + uint64(y), Width: 64, Height: 16, Format: format, Planes: planes}
}

func TestYUVComposition(t *testing.T) {
	for _, tc := range []struct {
		name      string
		p010, hdr bool
		coeff     uint8
		y, u, v   int
		rgb       [3]float64
	}{
		{"NV12 BT709 limited white", false, false, 2, 235, 128, 128, [3]float64{1, 1, 1}},
		{"NV12 BT709 limited red", false, false, 2, 63, 102, 240, [3]float64{1, 0, 0}},
		{"NV12 BT601 limited red", false, false, 4, 81, 90, 240, [3]float64{1, 0, 0}},
		{"P010 BT2020 limited white", true, false, 6, 940, 512, 512, [3]float64{1, 1, 1}},
		{"P010 BT2020 PQ 1000 nits", true, true, 6, 756, 512, 512, [3]float64{0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *Renderer
			if tc.hdr {
				r = hdrTestRenderer(t)
			} else {
				r = newTestRenderer(t, 64, 16)
				defer r.Close()
			}
			b := yuvTestBuffer(t, tc.p010, tc.y, tc.u, tc.v)
			if !slices.Contains(r.DMABuf().Formats, domain.DMABufFormat{Format: b.Format}) {
				t.Skipf("linear YUV %#x not importable", b.Format)
			}
			if distinct, err := differentBacking(b.Planes); err != nil || distinct {
				t.Fatalf("expected duplicate plane fds, distinct=%t err=%v", distinct, err)
			}
			if _, err := r.importDMABuf(b); err != nil {
				t.Fatalf("YUV import: %v", err)
			}
			c := domain.SurfaceContent{ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: b, Color: domain.SurfaceColor{Coefficients: tc.coeff, Range: 2, Chroma: 1}}
			if tc.hdr {
				c.Color.TF, c.Color.Primaries = domain.ColorTFPQ, domain.ColorPrimariesBT2020
			}
			scene := domain.Scene{Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
			if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: c}); err != nil {
				t.Fatal(err)
			}
			if tc.hdr {
				// Limited-range P010: PQ code is (Y-64)/876. Round-trip
				// through fp16 composition and HDR output without clipping.
				code := (float64(tc.y) - 64) / 876
				want := pqEncode(pqDecode(code))
				got := hdrTargetAt(t, r, 8, 8)
				for _, channel := range got {
					if math.Abs(channel-want) > .015 {
						t.Fatalf("PQ %v want %v", got, want)
					}
				}
				// SDR capture tone-maps the 1000-nit white onto the shoulder.
				linear := pqDecode(code) / 203
				sdr := captureSDR([3]float64{linear, linear, linear})
				if px := readPixels(t, r).RGBAAt(8, 8); !near(px, color.RGBA{sdr[0], sdr[1], sdr[2], 255}, 2) || px.A != 255 {
					t.Fatalf("capture %v want %v", px, sdr)
				}
				return
			}
			got := readPixels(t, r).RGBAAt(8, 8)
			want := color.RGBA{uint8(math.Round(tc.rgb[0] * 255)), uint8(math.Round(tc.rgb[1] * 255)), uint8(math.Round(tc.rgb[2] * 255)), 255}
			if !near(got, want, 8) {
				t.Errorf("got %v want %v", got, want)
			}
		})
	}
}

func pqDecode(code float64) float64 {
	p := math.Pow(code, 32.0/2523)
	return math.Pow(math.Max(p-3424.0/4096, 0)/(2413.0/128-p*2392.0/128), 16384.0/2610) * 10000
}

// Luma and chroma rows outside the crop must not bleed into its edge.
func TestYUVSourceCropEdges(t *testing.T) {
	r := newTestRenderer(t, 64, 16)
	defer r.Close()
	b := yuvTestBuffer(t, false, 235, 128, 128, [3]int{16, 16, 240})
	if !slices.Contains(r.DMABuf().Formats, domain.DMABufFormat{Format: b.Format}) {
		t.Skipf("linear NV12 not importable")
	}
	c := domain.SurfaceContent{ID: 1, Width: 64, Height: 16, LogicalW: 64, LogicalH: 16, Source: [4]float32{0, 4, 64, 8}, Opaque: true, DMABuf: b, Color: domain.SurfaceColor{Coefficients: 2, Range: 2, Chroma: 1}}
	scene := domain.Scene{Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	for _, y := range []int{0, 1, 8, 15} {
		if got := readPixels(t, r).RGBAAt(8, y); !near(got, color.RGBA{255, 255, 255, 255}, 8) {
			t.Errorf("cropped edge y=%d: %v, want white", y, got)
		}
	}
}

// Limited-range saturated red reconstructs slightly negative blue.
// Expanded decode must clamp that undershoot rather than feed it to pow.
func TestNV12SDRExpansionNegativeUndershoot(t *testing.T) {
	for _, single := range []bool{false, true} {
		name := "indexed"
		if single {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			r := hdrTestRendererVariant(t, single)
			b := yuvTestBuffer(t, false, 63, 102, 240)
			if !slices.Contains(r.DMABuf().Formats, domain.DMABufFormat{Format: b.Format}) {
				t.Skip("linear NV12 not importable")
			}
			if _, err := r.importDMABuf(b); err != nil {
				t.Fatal(err)
			}
			c := domain.SurfaceContent{ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: b, Color: domain.SurfaceColor{Coefficients: 2, Range: 2, Chroma: 1}}
			scene := domain.Scene{Seq: 1, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
			// Mirror the shader's BT.709 limited-range reconstruction exactly.
			y, cb, cr := (63.0-16)/219, (102.0-128)/224, (240.0-128)/224
			encoded := [3]float64{y + 2*(1-.2126)*cr, y - 2*.0722*(1-.0722)/(1-.2126-.0722)*cb - 2*.2126*(1-.2126)/(1-.2126-.0722)*cr, y + 2*(1-.0722)*cb}
			if encoded[2] >= 0 {
				t.Fatalf("fixture has no negative undershoot: %v", encoded)
			}
			for _, decode := range []domain.SDRDecode{domain.SDRDecodeGamma22, domain.SDRDecodeSRGB} {
				curve := (domain.SDRExpansion{Intensity: .5, PeakNits: 604, SDRWhiteNits: 203, Decode: decode}).Curve()
				scene.Seq++
				scene.Windows[0].SDRExpansion = curve
				if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: c}); err != nil {
					t.Fatal(err)
				}
				a, g, blue := curve.ApplyRGB(domain.DecodeSDR(encoded[0], decode), domain.DecodeSDR(encoded[1], decode), domain.DecodeSDR(encoded[2], decode))
				linear := [3]float64{a, g, blue}
				matrix := [3][3]float64{{.627404, .329283, .043313}, {.069097, .919540, .011362}, {.016391, .088013, .895595}}
				got := hdrTargetAt(t, r, 8, 8)
				for ch := range got {
					want := 0.0
					for k := range linear {
						want += matrix[ch][k] * linear[k]
					}
					actual := pqDecode(got[ch]) / 203
					if math.IsNaN(actual) || math.IsInf(actual, 0) || math.Abs(actual-want) > .025 {
						t.Errorf("%s channel %d readback %.6f mirror %.6f", decode, ch, actual, want)
					}
					t.Logf("%s BT2020 channel %d readback %.6f mirror %.6f", decode, ch, actual, want)
				}
			}
		})
	}
}
