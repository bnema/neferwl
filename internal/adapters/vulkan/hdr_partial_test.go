package vulkan

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestNextPQRect(t *testing.T) {
	whole := image.Rect(0, 0, 128, 96)
	for _, tc := range []struct {
		name        string
		valid       bool
		stale, want image.Rectangle
	}{
		{"new", false, image.Rect(2, 3, 8, 9), whole},
		{"unchanged", true, image.Rectangle{}, image.Rectangle{}},
		{"partial", true, image.Rect(2, 3, 8, 9), image.Rect(2, 3, 8, 9)},
		{"clipped", true, image.Rect(-4, -5, 140, 100), whole},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := target{pqValid: tc.valid, pqStale: tc.stale}
			if got := nextPQRect(&tg, whole); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	a, b := &target{pqValid: true}, &target{pqValid: true}
	d1, d2, d3 := image.Rect(1, 2, 8, 9), image.Rect(20, 30, 25, 35), image.Rect(40, 50, 45, 55)
	for i, step := range []struct {
		out          *target
		damage, want image.Rectangle
	}{
		{a, d1, d1}, {b, d2, d1.Union(d2)}, {a, d3, d2.Union(d3)},
	} {
		for _, tg := range []*target{a, b} {
			tg.pqStale = tg.pqStale.Union(step.damage)
		}
		if got := nextPQRect(step.out, whole); got != step.want {
			t.Fatalf("frame %d got %v want %v", i, got, step.want)
		}
		step.out.pqValid, step.out.pqStale = true, image.Rectangle{}
	}
}

func TestHDRPartialMatchesFullConversion(t *testing.T) {
	for _, transform := range []domain.BufferTransform{0, 1, 6} {
		t.Run(fmt.Sprintf("transform=%d", transform), func(t *testing.T) {
			const w, h = 128, 96
			newR := func() *Renderer {
				tw, th := transform.Size(w, h)
				r := newTestRenderer(t, tw, th)
				r.SetVirtualOutput(true)
				r.SetHDRReadback(true)
				r.SetHDR(203)
				bs, err := r.ExportTargets(2, nil, false)
				if err != nil {
					t.Skipf("HDR targets unavailable: %v", err)
				}
				t.Cleanup(func() {
					for _, b := range bs {
						for _, p := range b.Planes {
							_ = p.File.Close()
						}
					}
				})
				return r
			}
			partial, full := newR(), newR()
			pixels := make([]byte, 64*64*4)
			for i := 0; i < len(pixels); i += 4 {
				copy(pixels[i:], []byte{30, 20, 10, 255})
			}
			base := shmContent(t, 64, 64, 64*4, pixels)
			base.ID = 1
			scene := domain.Scene{Seq: 1, Transform: transform, OutputWidth: w, OutputHeight: h, Scale: 1, Background: "#1a2030", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{X: 8, Y: 8, W: 64, H: 64}}}}
			hist := []domain.SeqDamage{{Seq: 1, Full: true}}
			content := *base
			content.Seq, content.DamageHistory = 1, hist
			// Six alternating frames change small disjoint regions. Two extra unchanged
			// frames drain the other target's stale area and exercise an empty conversion.
			for i := range 8 {
				if i > 0 && i < 6 {
					x, y := i*7, i*5
					for yy := y; yy < y+4; yy++ {
						for xx := x; xx < x+4; xx++ {
							copy(pixels[(yy*64+xx)*4:], []byte{byte(40 * i), byte(200 - i*20), byte(15 * i), 255})
						}
					}
					hist = append(hist, domain.SeqDamage{Seq: uint64(i + 1), Rects: []domain.Rect{{X: x, Y: y, W: 4, H: 4}}})
					content = damagedContent(t, base.SHM.File, *base, uint64(i+1), pixels, hist)
				}
				contents := map[domain.WindowID]domain.SurfaceContent{1: content}
				partial.UseTarget(i % 2)
				full.UseTarget(i % 2)
				before := partial.redrawn
				if err := render(partial, scene, contents); err != nil {
					t.Fatal(err)
				}
				gotRect := partial.call.hdrPush
				if transform != 0 || i == 0 || i == 1 {
					if gotRect[0] != 0 || gotRect[1] != 0 || gotRect[2] != float32(partial.width) || gotRect[3] != float32(partial.height) {
						t.Fatalf("frame %d expected whole PQ rect: %v", i, gotRect)
					}
				} else if i == 7 {
					if gotRect[0] != gotRect[2] || gotRect[1] != gotRect[3] {
						t.Fatalf("unchanged frame expected empty PQ rect: %v", gotRect)
					}
				} else if (gotRect[2]-gotRect[0])*(gotRect[3]-gotRect[1]) >= w*h {
					t.Fatalf("frame %d expected partial PQ rect: %v", i, gotRect)
				}
				ref := scene
				ref.Seq = uint64(i + 100)
				// A new Seq alone permits sceneDelta. Equivalent color spellings
				// change the scene background and force a full redraw, without
				// changing its pixels or adding a test-only renderer control.
				if i%2 == 0 {
					ref.Background = "#1A2030"
				}
				fullBefore := full.redrawn
				if err := render(full, ref, contents); err != nil {
					t.Fatal(err)
				}
				if full.redrawn-fullBefore != w*h {
					t.Fatal("reference did not redraw in full")
				}
				if got := full.call.hdrPush; got[0] != 0 || got[1] != 0 || got[2] != float32(full.width) || got[3] != float32(full.height) {
					t.Fatalf("reference PQ rect %v", got)
				}
				a, b := partial.HDRPixels(), full.HDRPixels()
				if a == nil || b == nil || !slices.Equal(a.Pix, b.Pix) {
					t.Fatalf("frame %d PQ differs from full conversion", i)
				}
				if transform == 0 && i > 0 && partial.redrawn-before >= w*h {
					t.Fatalf("frame %d unexpectedly composed in full", i)
				}
			}
			// Changing brightness must invalidate both targets, including the one
			// not selected until the following frame.
			partial.SetHDR(250)
			full.SetHDR(250)
			for i := range 2 {
				if partial.targets[i].pqValid {
					t.Fatal("brightness change retained valid PQ target")
				}
			}
			for i := range 2 {
				partial.UseTarget(i)
				full.UseTarget(i)
				contents := map[domain.WindowID]domain.SurfaceContent{1: content}
				if err := render(partial, scene, contents); err != nil {
					t.Fatal(err)
				}
				ref := scene
				ref.Seq = uint64(i + 200)
				if i%2 == 0 {
					ref.Background = "#1A2030"
				}
				fullBefore := full.redrawn
				if err := render(full, ref, contents); err != nil {
					t.Fatal(err)
				}
				if full.redrawn-fullBefore != w*h {
					t.Fatal("reference did not redraw in full")
				}
				if !slices.Equal(partial.HDRPixels().Pix, full.HDRPixels().Pix) {
					t.Fatal("brightness change PQ differs")
				}
			}
		})
	}
}
