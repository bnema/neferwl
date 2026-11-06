package vulkan

import (
	vk "bnema.dev/purego-vulkan/vulkan"
	"image"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

func TestTrimCaptureResources(t *testing.T) {
	for _, mode := range []string{"sdr", "cursor", "hdr", "pq", "pq-cursor"} {
		t.Run(mode, func(t *testing.T) {
			var r *Renderer
			if mode == "hdr" || mode == "pq" || mode == "pq-cursor" {
				r = hdrTestRenderer(t)
			} else {
				r = newTestRenderer(t, 16, 8)
			}
			requireCaptureSync(t, r)
			if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
				t.Fatal(err)
			}
			trimSettled(t, r, time.Time{})
			baseline := openFDs(t)
			pq := mode == "pq" || mode == "pq-cursor"
			var cursor *domain.CaptureCursor
			if mode == "cursor" || mode == "pq-cursor" {
				cursor = &domain.CaptureCursor{X: 3, Y: 4, Image: domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 0, 255, 255}}}
			}
			for range 2 { // A capture immediately after trim recreates everything lazily.
				f, err := r.beginCaptureCursor(false, pq, cursor)
				if err != nil {
					t.Fatal(err)
				}
				waitCaptureSlot(t, r, f)
				if err := r.Trim(time.Time{}); err != nil {
					t.Fatal(err)
				}
				if len(r.captures) == 0 {
					t.Fatal("trim destroyed a leased capture")
				}
				dst := make([]byte, 16*8*4)
				if err := f.Read(image.Rect(0, 0, 16, 8), dst, 16*4); err != nil {
					t.Fatal(err)
				}
				r.EndCapture(f)
				if err := r.Trim(time.Time{}); err != nil {
					t.Fatal(err)
				}
				if len(r.captures) != 0 || len(r.dmaIdle) != 0 || r.capturePass.pipeline != 0 || r.captureSDR.image != 0 || r.capturePQ.pipeline != 0 || r.capturePQ.out.image != 0 {
					t.Fatal("capture resources survived trim")
				}
				if after := openFDs(t); after != baseline {
					t.Fatalf("fds baseline=%d after trim=%d", baseline, after)
				}
			}
		})
	}
}

func TestTrimCaptureDMACopies(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
		t.Fatal(err)
	}
	trimSettled(t, r, time.Time{})
	baseline := openFDs(t)
	buf := castLinear(t, r.gpu, domain.CastXRGB8888, 16, 8)
	retained := r.gpu.castImages[buf.ID]
	f := captureWait(t, r).(*captureFrame)
	castSubmit(t, f, image.Rect(0, 0, 16, 8), buf)
	r.gpu.ReleaseCast(buf)
	for _, p := range buf.Planes {
		_ = p.File.Close()
	}
	r.EndCapture(f)
	if err := r.Trim(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(r.captures) != 0 || len(r.dmaIdle) != 0 || retained.img != nil || retained.refs != 0 {
		t.Fatal("trim retained released cast or copy resources")
	}
	if after := openFDs(t); after != baseline {
		t.Fatalf("fds baseline=%d after trim=%d", baseline, after)
	}
}

func TestTrimCapturePendingFence(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
		t.Fatal(err)
	}
	f, err := r.beginCaptureAs(true, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCaptureSlot(t, r, f)
	r.EndCapture(f)
	saved := r.sync
	m := newMockcaptureSync(t)
	m.EXPECT().fenceStatus(f.slot.fence).Return(vk.Timeout).Once()
	r.sync = m
	if err := r.Trim(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(r.captures) == 0 {
		t.Fatal("trim destroyed pending capture")
	}
	r.sync = saved
	if err := r.Trim(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(r.captures) != 0 {
		t.Fatal("trim retained completed capture")
	}
}
