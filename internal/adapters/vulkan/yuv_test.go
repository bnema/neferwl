package vulkan

import (
	"image/color"
	"math"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Each plane can be backed by a separate DMA-BUF, as with VAAPI exports.
func yuvTestBuffer(t *testing.T, p010 bool, y, u, v int) *ports.DMABuf {
	t.Helper()
	format := fourcc('N', 'V', '1', '2')
	if p010 {
		format = fourcc('P', '0', '1', '0')
	}
	planes := make([]ports.DMABufPlane, 2)
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("udmabuf unavailable: %v", err)
	}
	defer dev.Close()
	for i := range planes {
		mem, err := unix.MemfdCreate("yuv-client", unix.MFD_ALLOW_SEALING)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unix.Close(mem) })
		stride := uint32(64)
		if p010 {
			stride = 128
		}
		rows := 16
		if i == 1 {
			rows = 8
		}
		size := int(stride) * rows
		pages := (size + os.Getpagesize() - 1) / os.Getpagesize() * os.Getpagesize()
		if err := unix.Ftruncate(mem, int64(pages)); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, size)
		for n := 0; n < size; {
			values := []int{y}
			if i == 1 {
				values = []int{u, v}
			}
			for _, val := range values {
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
		f := os.NewFile(fd, "yuv-client")
		t.Cleanup(func() { f.Close() })
		planes[i] = ports.DMABufPlane{File: f, Stride: stride}
	}
	return &ports.DMABuf{ID: uint64(format) + uint64(y), Width: 64, Height: 16, Format: format, Planes: planes}
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
				var err error
				r, err = New(64, 16)
				if err != nil {
					t.Skipf("Vulkan unavailable: %v", err)
				}
				defer r.Close()
			}
			b := yuvTestBuffer(t, tc.p010, tc.y, tc.u, tc.v)
			if !slices.Contains(r.DMABuf().Formats, ports.DMABufFormat{Format: b.Format}) {
				t.Skipf("linear YUV %#x lacks Vulkan disjoint import support", b.Format)
			}
			c := ports.SurfaceContent{ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: b, Color: ports.SurfaceColor{Coefficients: tc.coeff, Range: 2, Chroma: 1}}
			if tc.hdr {
				c.Color.TF, c.Color.Primaries = ports.ColorTFPQ, ports.ColorPrimariesBT2020
			}
			scene := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
			if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
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
				if px := r.Pixels().RGBAAt(8, 8); px != (color.RGBA{255, 255, 255, 255}) {
					t.Fatalf("capture %v", px)
				}
				return
			}
			got := r.Pixels().RGBAAt(8, 8)
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
