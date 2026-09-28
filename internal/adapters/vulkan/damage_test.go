package vulkan

import (
	"bytes"
	"fmt"
	"image/color"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// damagedContent writes px into the content's pool (the client drawing
// into the same buffer) and records the damage of the new Seq.
func damagedContent(t *testing.T, f *os.File, base ports.SurfaceContent, seq uint64, px []byte, hist []ports.SeqDamage) ports.SurfaceContent {
	t.Helper()
	if _, err := f.WriteAt(px, 0); err != nil {
		t.Fatal(err)
	}
	c := base
	c.Seq, c.DamageHistory = seq, hist
	return c
}

// Partial redraws are pixel-identical to full ones: with both targets
// ages (A then B, then one render) and with shm double buffers
// (render, skip, render).
func TestRendererDamageMatchesFullRedraw(t *testing.T) {
	for _, tr := range []ports.BufferTransform{0, 1, 6} {
		t.Run(fmt.Sprintf("transform %d", tr), func(t *testing.T) { damageMatchesFullRedraw(t, tr) })
	}
}

// damageMatchesFullRedraw checks partial frames of a window whose buffer
// carries transform tr against full redraws.
func damageMatchesFullRedraw(t *testing.T, tr ports.BufferTransform) {
	const w, h = 32, 32
	newR := func() *Renderer {
		r, err := New(64, 48)
		if err != nil {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Cleanup(r.Close)
		return r
	}
	damaged, full := newR(), newR()
	bufsD, err := damaged.ExportTargets(2, nil)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	for _, b := range bufsD {
		b.Planes[0].File.Close()
	}
	px := func(c color.RGBA, x0, y0, x1, y1 int, into []byte) []byte {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				copy(into[(y*w+x)*4:], []byte{c.B, c.G, c.R, 255})
			}
		}
		return into
	}
	pixels := px(color.RGBA{10, 20, 30, 255}, 0, 0, w, h, make([]byte, w*h*4))
	base := shmContent(t, w, h, w*4, pixels)
	base.ID, base.Transform = 1, tr
	f := base.SHM.File
	scene := ports.Scene{Seq: 5, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: w, H: h}}}}
	hist := []ports.SeqDamage{{Seq: 1, Full: true}}
	c := damagedContent(t, f, *base, 1, pixels, hist)
	step := func(i int, c ports.SurfaceContent) {
		t.Helper()
		damaged.UseTarget(i % 2)
		contents := map[ports.WindowID]ports.SurfaceContent{1: c}
		if err := render(damaged, scene, contents); err != nil {
			t.Fatal(err)
		}
		if err := render(full, ports.Scene{Background: scene.Background, Windows: scene.Windows}, contents); err != nil {
			t.Fatal(err)
		}
		if a, b := damaged.Pixels(), full.Pixels(); !bytes.Equal(a.Pix, b.Pix) {
			t.Fatalf("frame %d differs from a full redraw", i)
		}
	}
	step(0, c)
	step(1, c) // target 1 drawn whole once
	before := damaged.redrawn
	// A: a red square; B: a green one elsewhere. Target 0 holds Seq 1,
	// so its next frame redraws the union of both.
	for i, d := range []struct {
		col  color.RGBA
		rect ports.Rect
	}{{color.RGBA{255, 0, 0, 255}, ports.Rect{X: 2, Y: 2, W: 4, H: 4}}, {color.RGBA{0, 255, 0, 255}, ports.Rect{X: 20, Y: 10, W: 3, H: 5}}} {
		seq := uint64(2 + i)
		px(d.col, d.rect.X, d.rect.Y, d.rect.X+d.rect.W, d.rect.Y+d.rect.H, pixels)
		hist = append(hist, ports.SeqDamage{Seq: seq, Rects: []ports.Rect{d.rect}})
		c = damagedContent(t, f, *base, seq, pixels, hist)
	}
	step(2, c)
	if got := damaged.redrawn - before; tr == 0 && got >= 64*48 {
		t.Fatalf("partial frame redrew %d pixels", got)
	}
	// Render, skip (no frame), render: target 1 is two contents behind.
	px(color.RGBA{0, 0, 255, 255}, 0, 30, 2, 32, pixels)
	hist = append(hist, ports.SeqDamage{Seq: 4, Rects: []ports.Rect{{Y: 30, W: 2, H: 2}}})
	c = damagedContent(t, f, *base, 4, pixels, hist)
	step(3, c)
	step(4, c)
}

// A window left out of a frame (on an overlay plane) and drawn again in
// the same scene is redrawn whole; one held but no longer drawn is
// cleared.
func TestRendererDamageWindowLeftOutAndBack(t *testing.T) {
	newR := func() *Renderer {
		r, err := New(64, 48)
		if err != nil {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Cleanup(r.Close)
		return r
	}
	damaged, full := newR(), newR()
	c := solidContent(t, 16, 16, color.RGBA{200, 10, 10, 255})
	c.ID, c.Seq = 1, 1
	contents := map[ports.WindowID]ports.SurfaceContent{1: c}
	with := ports.Scene{Seq: 9, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: 16, H: 16}}}}
	without := with
	without.Windows = nil
	check := func(s ports.Scene, what string) {
		t.Helper()
		if err := render(damaged, s, contents); err != nil {
			t.Fatal(err)
		}
		plain := s
		plain.Seq = 0
		if err := render(full, plain, contents); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(damaged.Pixels().Pix, full.Pixels().Pix) {
			t.Fatalf("%s differs from a full redraw", what)
		}
	}
	check(without, "overlay frame")
	check(with, "overlay refused, window composed")
	check(without, "window on the overlay again")
}
