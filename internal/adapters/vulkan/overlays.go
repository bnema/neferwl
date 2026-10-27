package vulkan

import (
	"image"
	"math"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"github.com/bnema/neferwl/internal/domain"
)

// overlays draws scene overlays over everything, after the capture
// indicators. CPU rasters use the SHM copy path, cached by overlay ID, scale
// and style; Seq versions the content held by the GPU's double buffer.
func (w *sceneWalk) overlays() {
	for _, o := range w.s.Overlays {
		if o.Window == 0 {
			w.overlay(o, w.bounds, w.bounds, w.bounds.Dx())
		}
	}
}

// overlay places a cached raster relative to anchor, clipping its GPU quad
// without changing the raster dimensions or cache key.
func (w *sceneWalk) overlay(o domain.Overlay, anchor, clip image.Rectangle, outputWidth int) {
	r := w.r
	switch o.Kind {
	case domain.OverlayLabel:
	case domain.OverlayPanel:
		if o.Panel == nil {
			return
		}
	default:
		return
	}
	bg, fg := overlaydraw.Colors(w.s.Border.Active, o.Style.Background, o.Style.Foreground)
	style := overlaydraw.Style{FontPx: overlaydraw.FontPixels(w.s.Physical.Width, w.s.Physical.Height, w.s.Physical.MMWidth, w.s.Physical.MMHeight, w.scale, o.Style.Size), Background: bg, Foreground: fg}
	key := shmKey{overlay: o.ID, scale: w.scale, style: style}
	if o.Kind == domain.OverlayPanel {
		key.maxWidth = max(1, outputWidth-2*w.phys(24))
	}
	var pix []byte
	var lw, lh int
	var c *shmCopy
	if o.Kind == domain.OverlayPanel {
		// SHM already records dimensions for each cached sequence: check it
		// before touching the panel's reusable CPU raster.
		if surface := r.shm[key]; surface != nil {
			for _, b := range surface.bufs {
				if b != nil && b.valid && b.holds.seq == o.Seq {
					lw, lh = b.holds.w, b.holds.h
					c = r.shmCached(key, b.holds)
					break
				}
			}
		}
		if c == nil {
			if r.panels == nil {
				r.panels = make(map[uint64]*overlaydraw.Panel)
			}
			p := r.panels[o.ID]
			if p == nil {
				p = &overlaydraw.Panel{}
				r.panels[o.ID] = p
			}
			pix, lw, lh = p.Draw(o.Panel.Lines, o.Panel.Graph, w.scale, key.maxWidth, style)
		}
	} else {
		pix, lw, lh = r.overlayRaster.Label(o.Text, o.Typed, w.scale, style)
	}
	if lw <= 0 || lh <= 0 {
		return
	}
	// Anchored outside the target's top-left corner, overlapping it by a
	// quarter of the label height, then kept inside the output.
	inset := int(math.Round(0.25 * float64(lh)))
	x := min(max(w.phys(o.Rect.X)-lw+inset, w.bounds.Min.X), max(w.bounds.Max.X-lw, w.bounds.Min.X))
	y := min(max(w.phys(o.Rect.Y)-lh+inset, w.bounds.Min.Y), max(w.bounds.Max.Y-lh, w.bounds.Min.Y))
	if o.Anchor == domain.AnchorTopCenter {
		x = min(max((w.bounds.Dx()-lw)/2+w.bounds.Min.X, w.bounds.Min.X), max(w.bounds.Max.X-lw, w.bounds.Min.X))
		y = min(max(w.phys(o.Rect.Y), w.bounds.Min.Y), max(w.bounds.Max.Y-lh, w.bounds.Min.Y))
	}
	if o.Window != 0 {
		x = anchor.Min.X + w.phys(o.Rect.X) - lw + inset
		y = anchor.Min.Y + w.phys(o.Rect.Y) - lh + inset
		if o.Anchor == domain.AnchorTopCenter {
			x = anchor.Min.X + (anchor.Dx()-lw)/2
			y = anchor.Min.Y + w.phys(o.Rect.Y)
		}
	}
	rect := image.Rect(x, y, x+lw, y+lh)
	if rect.Intersect(clip).Empty() {
		return
	}
	st := shmState{w: lw, h: lh, seq: o.Seq}
	c = r.shmCached(key, st)
	if c == nil {
		var err error
		if c, err = r.shmCopyFor(key, st, pix, 0, lw*4, nil); err != nil {
			return
		}
	}
	dr := r.contentDraw(rect.Intersect(clip), rect, lw, lh, [4]float32{}, 0, modeBuffer, false)
	dr.pc.color[3] = w.alpha
	dr.shm = c
	dr.pc.buf = [4]uint32{0, uint32(lw), uint32(lh), 0}
	w.draws = append(w.draws, dr)
}
