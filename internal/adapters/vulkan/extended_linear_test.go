package vulkan

import (
	"encoding/binary"
	"image/color"
	"math"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

func TestExtendedLinearClient(t *testing.T) {
	for _, hdr := range []bool{false, true} {
		t.Run(map[bool]string{false: "SDR", true: "HDR"}[hdr], func(t *testing.T) {
			var r *Renderer
			if hdr {
				r = hdrTestRenderer(t)
			} else {
				var err error
				r, err = New(64, 16)
				if err != nil {
					t.Skipf("Vulkan unavailable: %v", err)
				}
				defer r.Close()
			}
			format := ports.DMABufFormat{Format: fourcc('A', 'B', '4', 'H'), Modifier: 0}
			if !slices.Contains(r.DMABuf().Formats, format) {
				t.Skip("ABGR16161616F linear import unavailable")
			}
			// 1.0 = 80 nits, 12.5 = 1000 nits, -0.25 is out of gamut.
			pixels := make([]byte, 64*16*8)
			for y := 0; y < 16; y++ {
				for x := 0; x < 64; x++ {
					v := uint16(0x3c00)
					if x >= 20 && x < 40 {
						v = 0x4a40
					}
					if x >= 40 {
						v = 0xb400
					}
					i := (y*64 + x) * 8
					for ch := 0; ch < 3; ch++ {
						binary.LittleEndian.PutUint16(pixels[i+ch*2:], v)
					}
					binary.LittleEndian.PutUint16(pixels[i+6:], 0x3c00)
				}
			}
			mem, err := unix.MemfdCreate("fp16-client", unix.MFD_ALLOW_SEALING)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(mem)
			if err := unix.Ftruncate(mem, int64(len(pixels))); err != nil {
				t.Fatal(err)
			}
			if _, err := unix.Pwrite(mem, pixels, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
				t.Fatal(err)
			}
			dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
			if err != nil {
				t.Skipf("udmabuf unavailable: %v", err)
			}
			defer dev.Close()
			arg := struct {
				memfd, flags uint32
				offset, size uint64
			}{memfd: uint32(mem), flags: 1, size: uint64(len(pixels))}
			fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), 0x40187542, uintptr(unsafe.Pointer(&arg)))
			if errno != 0 {
				t.Skipf("udmabuf create: %v", errno)
			}
			f := os.NewFile(fd, "fp16-client")
			defer f.Close()
			buf := &ports.DMABuf{ID: 164, Width: 64, Height: 16, Format: format.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 512}}}
			c := ports.SurfaceContent{ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: buf, Color: ports.SurfaceColor{TF: ports.ColorTFExtendedLinear, Primaries: ports.ColorPrimariesSRGB}}
			scene := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
			if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
				t.Fatal(err)
			}
			if hdr {
				for _, tc := range []struct {
					x    int
					nits float64
				}{{5, 80}, {25, 1000}, {45, 0}} {
					got := hdrTargetAt(t, r, tc.x, 8)
					want := pqEncode(tc.nits)
					for i, v := range got {
						if math.Abs(v-want) > .009 {
							t.Errorf("x=%d channel=%d PQ=%.4f want %.4f", tc.x, i, v, want)
						}
					}
				}
			} else {
				for _, tc := range []struct {
					x int
					v byte
				}{{5, 168}, {25, 255}, {45, 0}} {
					got := readPixels(t, r).RGBAAt(tc.x, 8)
					if !near(got, color.RGBA{tc.v, tc.v, tc.v, 255}, 3) {
						t.Errorf("x=%d got %v want %d", tc.x, got, tc.v)
					}
				}
			}
		})
	}
}
