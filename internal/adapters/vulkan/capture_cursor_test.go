package vulkan

import (
	"context"
	"encoding/binary"
	"golang.org/x/sys/unix"
	"image"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
)

func TestCaptureCursorPrivateComposite(t *testing.T) {
	for _, hdr := range []bool{false, true} {
		for _, pq := range []bool{false, true} {
			if pq && !hdr {
				continue
			}
			t.Run(map[bool]string{false: "SDR", true: "HDR"}[hdr]+map[bool]string{false: "", true: "PQ"}[pq], func(t *testing.T) {
				r := newTestRenderer(t, 8, 8)
				defer r.Close()
				if hdr {
					r.SetHDR(200)
					if r.hdrError != nil {
						t.Skipf("HDR unavailable: %v", r.hdrError)
					}
					r.SetHDRReadback(true)
					bufs, err := r.ExportTargets(1, r.gpu.hdrReadbackMods, false)
					if err != nil {
						t.Skipf("HDR target unavailable: %v", err)
					}
					defer func() {
						for _, b := range bufs {
							for _, p := range b.Planes {
								_ = p.File.Close()
							}
						}
					}()
					r.UseTarget(0)
				}
				if err := render(r, domain.Scene{Background: "#000000"}, nil); err != nil {
					t.Fatal(err)
				}
				cursor := &domain.CaptureCursor{X: 3, Y: 4, Image: domain.CursorImage{W: 2, H: 1, HotX: 1, Pixels: []byte{0, 0, 255, 255, 0, 0, 0, 0}}}
				// The debug lease uses the very same recording path without requiring
				// sync-file export, so software Vulkan also checks cursor composition.
				f, err := r.beginCaptureCursor(true, pq, cursor)
				if err != nil {
					t.Fatal(err)
				}
				waitCaptureSlot(t, r, f)
				dst := make([]byte, 8)
				if err = f.Read(image.Rect(2, 4, 4, 5), dst, 8); err != nil {
					t.Fatal(err)
				}
				if pq {
					word := binary.LittleEndian.Uint32(dst)
					want := PackXB30(uint16(pqEncode(200*0.627404)*1023+0.5), uint16(pqEncode(200*0.069097)*1023+0.5), uint16(pqEncode(200*0.016391)*1023+0.5))
					for shift := uint(0); shift < 30; shift += 10 {
						got, w := (word>>shift)&1023, (want>>shift)&1023
						if int(got)-int(w) > 2 || int(w)-int(got) > 2 {
							t.Fatalf("PQ got %#x want %#x", word, want)
						}
					}
				} else if dst[2] < 240 || dst[0] != 0 || dst[1] != 0 || dst[3] != 255 {
					t.Fatalf("cursor: %v", dst)
				}
				if dst[4] != 0 || dst[5] != 0 || dst[6] != 0 {
					t.Fatalf("transparent cursor pixel: %v", dst)
				}
				r.EndCapture(f)
				// Neither the display nor later cursor-free leases retain the cursor.
				f, err = r.beginCaptureAs(true, pq)
				if err != nil {
					t.Fatal(err)
				}
				waitCaptureSlot(t, r, f)
				if err = f.Read(image.Rect(2, 4, 3, 5), dst[:4], 4); err != nil {
					t.Fatal(err)
				}
				if pq {
					if binary.LittleEndian.Uint32(dst[:4])&0x3fffffff != 0 {
						t.Fatalf("cursor leaked: %v", dst)
					}
				} else if dst[0] != 0 || dst[1] != 0 || dst[2] != 0 {
					t.Fatalf("cursor leaked: %v", dst)
				}
				r.EndCapture(f)
			})
		}
	}
}

// waitCaptureSlot waits on the real GPU fence; no timer or polling is needed.
func waitCaptureSlot(t *testing.T, r *Renderer, f *captureFrame) {
	t.Helper()
	if err := checked("capture cursor test fence", r.gpu.dd.WaitForFences(r.gpu.device, 1, &f.slot.fence, 1, ^uint64(0))); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureCursorSyncLease(t *testing.T) {
	r := newTestRenderer(t, 4, 4)
	defer r.Close()
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Background: "#000000"}, nil); err != nil {
		t.Fatal(err)
	}
	c := &domain.CaptureCursor{X: 1, Y: 1, Image: domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 255, 0, 255}}}
	f, err := r.BeginCaptureCursor(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.Done() != nil {
		if err = syncfile.Wait(context.Background(), f.Done()); err != nil {
			t.Fatal(err)
		}
	}
	dst := make([]byte, 4)
	if err = f.Read(image.Rect(1, 1, 2, 2), dst, 4); err != nil {
		t.Fatal(err)
	}
	if dst[1] != 255 {
		t.Fatalf("cursor: %v", dst)
	}
	r.EndCapture(f)
}

func TestCaptureCursorCopyTo(t *testing.T) {
	r := newTestRenderer(t, 8, 8)
	defer r.Close()
	requireCaptureSync(t, r)
	if !slices.Contains(r.gpu.CaptureFormats(), domain.DMABufFormat{Format: fourcc('X', 'R', '2', '4')}) {
		t.Skip("linear capture destination unavailable")
	}
	file, mem := udmabufMem(t, 4096)
	if err := render(r, domain.Scene{Background: "#000000"}, nil); err != nil {
		t.Fatal(err)
	}
	c := &domain.CaptureCursor{X: 3, Y: 4, Image: domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 255, 0, 255}}}
	f, err := r.BeginCaptureCursor(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.EndCapture(f)
	dst := domain.DMABuf{Width: 2, Height: 1, Format: fourcc('X', 'R', '2', '4'), Planes: []domain.DMABufPlane{{File: file, Stride: 256}}}
	done, err := f.CopyTo(image.Rect(3, 4, 5, 5), dst)
	if err != nil {
		t.Fatal(err)
	}
	if done != nil {
		defer done.Close()
		if err = syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	}
	raw := make([]byte, 8)
	if _, err = unix.Pread(mem, raw, 0); err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0 || raw[1] != 255 || raw[2] != 0 || raw[3] != 255 || raw[4] != 0 || raw[5] != 0 || raw[6] != 0 {
		t.Fatalf("dmabuf pixels: %v", raw)
	}
}
