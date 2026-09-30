package vulkan

import (
	"image"
	"math"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

// sceneWalk turns one scene into quads in paint order, copying new wl_shm
// content into GPU buffers on the way. It lives for one frame.
type sceneWalk struct {
	r        *Renderer
	s        ports.Scene
	contents map[ports.WindowID]ports.SurfaceContent
	dmg      *damageRegion
	// scale maps the scene's logical pixels to the target's physical ones.
	scale float64
	// zoom shrinks the surfaces of the window being placed: an overview
	// preview (ports.SceneWindow.Preview); 1 otherwise.
	zoom   float64
	bounds image.Rectangle
	draws  []draw
}

// draws walks the scene into quads in paint order.
func (r *Renderer) draws(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent, dmg *damageRegion) []draw {
	clear(r.scratchDraws) // release references from longer earlier scenes
	w := &sceneWalk{r: r, s: s, contents: contents, dmg: dmg, scale: s.Scale, zoom: 1, bounds: image.Rect(0, 0, r.width, r.height), draws: r.scratchDraws[:0]}
	r.scratchCovers = r.scratchCovers[:0]
	if w.scale <= 0 {
		w.scale = 1
	}
	w.layers(false)
	outputBounds := w.bounds
	if s.WorkspaceClip != (ports.Rect{}) {
		c := s.WorkspaceClip
		w.bounds = w.bounds.Intersect(w.physRect(c.X, c.Y, c.W, c.H))
	}
	w.windows()
	w.popups(false)
	w.bounds = outputBounds
	w.layers(true)
	w.popups(true)
	w.captureIndicators()
	r.scratchDraws = w.draws
	return w.draws
}

// captureIndicators draws the compositor's capture indicator over everything
// (ports.CaptureIndicator). The border is four fills on the inside edge of the
// target, so it stays visible on a monitor edge; the pill is a small rounded
// square. Sizes are logical like the rest of the scene and scale with it. A
// scene without indicators draws nothing: captures are rendered from scenes
// that never carry them.
func (w *sceneWalk) captureIndicators() {
	if len(w.s.CaptureIndicators) == 0 {
		return
	}
	col := parseColor(ports.CaptureBorderColor)
	for _, m := range w.s.CaptureIndicators {
		t := m.Rect
		if t.W <= 0 || t.H <= 0 {
			continue
		}
		if m.Pill {
			w.pill(w.physRect(t.X, t.Y, t.W, t.H), col)
			continue
		}
		b := ports.CaptureBorderOf(t.W, t.H)
		if b <= 0 {
			// Too thin for a border (core inflates such marks, so this is a
			// 1-pixel output): a listed mark always draws something.
			w.fill(w.physRect(t.X, t.Y, t.W, t.H), col)
			continue
		}
		w.fill(w.physRect(t.X, t.Y, t.W, b), col)
		w.fill(w.physRect(t.X, t.Y+t.H-b, t.W, b), col)
		w.fill(w.physRect(t.X, t.Y+b, b, t.H-2*b), col)
		w.fill(w.physRect(t.X+t.W-b, t.Y+b, b, t.H-2*b), col)
	}
}

// pill fills a rounded square (physical pixels) with three rects: a full
// middle and two bands narrowed by the corner radius.
func (w *sceneWalk) pill(r image.Rectangle, c [3]uint8) {
	rad := min(w.phys(ports.CapturePillRadius), r.Dx()/2, r.Dy()/2)
	if rad <= 0 {
		w.fill(r, c)
		return
	}
	cut := (rad + 1) / 2
	w.fill(image.Rect(r.Min.X+cut, r.Min.Y, r.Max.X-cut, r.Min.Y+rad), c)
	w.fill(image.Rect(r.Min.X, r.Min.Y+rad, r.Max.X, r.Max.Y-rad), c)
	w.fill(image.Rect(r.Min.X+cut, r.Max.Y-rad, r.Max.X-cut, r.Max.Y), c)
}

func (w *sceneWalk) phys(v int) int { return int(math.Round(float64(v) * w.scale)) }

func (w *sceneWalk) physRect(x, y, width, height int) image.Rectangle {
	return image.Rect(w.phys(x), w.phys(y), w.phys(x+width), w.phys(y+height))
}

// physRectF is physRect for fractional logical coordinates (zoomed surfaces).
func (w *sceneWalk) physRectF(x, y, width, height float64) image.Rectangle {
	p := func(v float64) int { return int(math.Round(v * w.scale)) }
	return image.Rect(p(x), p(y), p(x+width), p(y+height))
}

// fill draws a solid rect (physical pixels).
func (w *sceneWalk) fill(rect image.Rectangle, c [3]uint8) {
	rect = rect.Intersect(w.bounds)
	if rect.Empty() {
		return
	}
	w.draws = append(w.draws, w.r.fillDraw(rect, c))
}

// dim paints a translucent black quad over rect (physical), clipped to the
// output. Damage clipping later limits it to the region being repainted,
// just like other fills.
func (w *sceneWalk) dim(rect image.Rectangle, alpha float64) {
	if rect = rect.Intersect(w.bounds); !rect.Empty() {
		w.draws = append(w.draws, w.r.dimDraw(rect, alpha))
	}
}

// layers draws the layer surfaces below the windows, or above them.
// The scene holds only the shown ones: core hides them under a fullscreen
// window.
func (w *sceneWalk) layers(afterWindows bool) {
	for _, layer := range w.s.Layers {
		if (layer.Layer >= ports.LayerTop) != afterWindows || layer.Rect.W <= 0 || layer.Rect.H <= 0 {
			continue
		}
		content := w.contents[layer.ID]
		w.dmg.window(layer.ID, content, w.physRect(layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H))
		w.place(layer.ID, &content, layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H)
	}
}

// windows draws ordered placements with borders. Tile lines go after
// the column group, before the first float above it; an overview preview
// of a float is not one.
func (w *sceneWalk) windows() {
	tileLines := false
	for _, win := range w.s.Windows {
		if win.Hidden || win.Popup || win.Rect.W <= 0 || win.Rect.H <= 0 {
			continue
		}
		if !win.Below && win.Floating && win.Preview == 0 && !tileLines {
			w.separators(0)
			tileLines = true
			if w.s.Dim > 0 {
				w.dim(w.bounds, w.s.Dim)
			}
		}
		// Content sits inside the border; core sized the client to match.
		b, inset := 0, ports.Sides(0)
		if !win.Fullscreen {
			b, inset = w.s.Border.Width, win.Inset
		}
		c := win.Rect.Inset(inset, b)
		body := w.physRect(c.X, c.Y, c.W, c.H)
		content := w.contents[win.ID]
		w.dmg.window(win.ID, content, w.physRect(win.Rect.X, win.Rect.Y, win.Rect.W, win.Rect.H))
		// Until its first buffer, a window shows the background: no flash.
		w.fill(body, parseColor(w.s.Background))
		if !content.Empty() {
			if win.Preview > 0 {
				w.zoom = win.Preview
			}
			w.place(win.ID, &content, c.X, c.Y, c.W, c.H)
			w.zoom = 1
		}
		if win.Floating {
			w.separators(win.ID)
		}
		if win.Dim > 0 {
			// A stashed window peeking in: dimmed with its border.
			w.dim(w.physRect(win.Rect.X, win.Rect.Y, win.Rect.W, win.Rect.H), win.Dim)
		}
	}
	if !tileLines {
		w.separators(0)
	}
}

// separators draws a window's border lines in order, active lines over
// inactive ones.
func (w *sceneWalk) separators(id ports.WindowID) {
	for _, sep := range w.s.Separators {
		if sep.Window != id {
			continue
		}
		col := w.s.Border.Inactive
		if sep.Active {
			col = w.s.Border.Active
		}
		if col != "" {
			w.fill(w.physRect(sep.Rect.X, sep.Rect.Y, sep.Rect.W, sep.Rect.H), parseColor(col))
		}
	}
}

// popups draws only what the client drew, shadows clipped. Window popups
// stay with the windows; a layer's go over every layer.
func (w *sceneWalk) popups(overLayers bool) {
	for _, win := range w.s.Windows {
		if win.Popup && win.OverLayers == overLayers && !win.Hidden && win.Rect.W > 0 && win.Rect.H > 0 {
			content := w.contents[win.ID]
			w.dmg.window(win.ID, content, w.physRect(win.Rect.X, win.Rect.Y, win.Rect.W, win.Rect.H))
			w.place(win.ID, &content, win.Rect.X, win.Rect.Y, win.Rect.W, win.Rect.H)
		}
	}
}

// place draws a surface tree with its window geometry at (x, y) logical,
// clipped to width×height logical: client shadows fall outside. Every
// surface of the tree is keyed by its stable surface identity.
func (w *sceneWalk) place(id ports.WindowID, content *ports.SurfaceContent, x, y, width, height int) {
	clip := image.Rect(x, y, x+width, y+height)
	// Offsets inside the tree shrink with a zoomed (preview) window.
	z := w.zoom
	ox, oy := float64(x)-float64(content.Geometry.X)*z, float64(y)-float64(content.Geometry.Y)*z
	at := func(ch *ports.Subsurface) (float64, float64) { return ox + float64(ch.X)*z, oy + float64(ch.Y)*z }
	covers := w.opaqueChildren(content, ox, oy, clip)
	for i := range content.Children {
		if ch := &content.Children[i]; ch.Below {
			cx, cy := at(ch)
			w.surface(&ch.SurfaceContent, cx, cy, clip, shmKey{id, ch.Surface}, ch.Version, covers, false)
		}
	}
	w.surface(content, ox, oy, clip, shmKey{id, content.Surface}, content.Version, covers, true)
	for i := range content.Children {
		if ch := &content.Children[i]; !ch.Below {
			cx, cy := at(ch)
			w.surface(&ch.SurfaceContent, cx, cy, clip, shmKey{id, ch.Surface}, ch.Version, nil, false)
		}
	}
}

// opaqueChildren gives the physical rects shown by the opaque subsurfaces
// above the root (e.g. a game's GPU surface over its window's shm buffer):
// they hide the surfaces below them. A child whose buffer will not draw is
// left out, so the surface below it still shows: a dmabuf that fails to
// import (opaqueChildren pre-imports it) or an unreadable shm buffer.
func (w *sceneWalk) opaqueChildren(content *ports.SurfaceContent, ox, oy float64, clip image.Rectangle) []image.Rectangle {
	covers := w.r.scratchCovers[:0]
	for i := range content.Children {
		ch := &content.Children[i]
		if ch.Below || !ch.Opaque {
			continue
		}
		_, dst, ok := w.surfaceRects(&ch.SurfaceContent, ox+float64(ch.X)*w.zoom, oy+float64(ch.Y)*w.zoom, clip)
		if !ok || !w.drawable(&ch.SurfaceContent) {
			continue
		}
		covers = append(covers, dst)
	}
	w.r.scratchCovers = covers
	return covers
}

// drawable reports whether the child's buffer will draw. GPU buffer
// imports are cached, so pre-importing here costs nothing.
func (w *sceneWalk) drawable(c *ports.SurfaceContent) bool {
	if c.DMABuf != nil {
		_, err := w.r.importDMABuf(c.DMABuf)
		return err == nil
	}
	b := c.SHM
	if b == nil {
		return false
	}
	_, err := w.r.shmPixels(b, b.Offset+(c.Height-1)*b.Stride+c.Width*4)
	return err == nil
}

// surfaceRects gives the physical rect of a surface buffer with its origin
// at (x, y) logical, shrunk by the walk's zoom, and the part of it clip
// (logical) shows. ok is false when there is nothing to draw.
func (w *sceneWalk) surfaceRects(content *ports.SurfaceContent, x, y float64, clip image.Rectangle) (full, dst image.Rectangle, ok bool) {
	lw, lh := content.LogicalW, content.LogicalH
	if lw <= 0 || lh <= 0 {
		lw, lh = content.Width, content.Height
	}
	if content.SHM == nil && content.DMABuf == nil || lw <= 0 || lh <= 0 || content.Width <= 0 || content.Height <= 0 {
		return full, dst, false
	}
	if content.DMABuf == nil && (content.SHM.Stride < content.Width*4 || content.SHM.Offset < 0) {
		return full, dst, false
	}
	full = w.physRectF(x, y, float64(lw)*w.zoom, float64(lh)*w.zoom)
	// A buffer drawn at the physical size (fractional-scale clients round
	// w*scale, we round per edge) is copied 1:1, never resampled for 1px.
	near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
	sourceW, sourceH := content.Width, content.Height
	if content.Source[2] > 0 {
		sourceW, sourceH = int(content.Source[2]), int(content.Source[3])
	}
	if content.Transform.Rotated() {
		sourceW, sourceH = sourceH, sourceW
	}
	if near(full.Dx(), sourceW) && near(full.Dy(), sourceH) {
		full.Max = full.Min.Add(image.Pt(sourceW, sourceH))
	}
	dst = full.Intersect(w.physRect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy())).Intersect(w.bounds)
	return full, dst, !dst.Empty()
}

// surface draws one surface buffer with its origin at (x, y) logical,
// clipped to clip (logical), unless an opaque surface above it (covers,
// physical) hides all of it: its buffer is then neither copied nor drawn.
func (w *sceneWalk) surface(content *ports.SurfaceContent, x, y float64, clip image.Rectangle, key shmKey, seq uint64, covers []image.Rectangle, root bool) {
	full, dst, ok := w.surfaceRects(content, x, y, clip)
	if !ok {
		return
	}
	for _, c := range covers {
		if dst.In(c) {
			return
		}
	}
	if root {
		w.dmg.content(key.win, content, full, dst)
	}
	w.content(dst, full, content, key, seq, root)
}

// content draws the part of a buffer mapped onto full (physical pixels)
// that dst shows. key names the surface, seq its content.
func (w *sceneWalk) content(dst, full image.Rectangle, content *ports.SurfaceContent, key shmKey, seq uint64, root bool) {
	r := w.r
	rect := dst
	if rect.Empty() {
		return
	}
	if content.DMABuf != nil {
		im, err := r.importDMABuf(content.DMABuf)
		if err != nil {
			// The window shows its background until a buffer imports.
			return
		}
		dr := r.contentDraw(rect, full, content.Width, content.Height, content.Source, content.Transform, modeImage, content.Opaque)
		r.setContentColor(&dr, content)
		if im.yuv {
			dr.pc.misc[1] |= flagYUV
			if content.DMABuf.Format == fourcc('P', '0', '1', '0') {
				dr.pc.misc[1] |= flagP010
			}
			coeff, ran := content.Color.Coefficients, content.Color.Range
			if coeff == 0 {
				coeff = 2 /* BT.709 */
			}
			if ran == 0 {
				ran = 2 /* limited */
			}
			dr.pc.buf[3] = uint32(coeff) | uint32(ran)<<8 | uint32(content.Color.Chroma)<<16
		}
		dr.set, dr.im = im.set, im
		dr.acquire = content.Acquire
		w.draws = append(w.draws, dr)
		return
	}
	b := content.SHM
	pixels, err := r.shmPixels(b, b.Offset+(content.Height-1)*b.Stride+content.Width*4)
	if err != nil {
		return
	}
	st := shmState{w: content.Width, h: content.Height, seq: seq, windowSeq: content.Seq}
	// Damage history is the root surface's: children copy in full.
	var damage func(uint64) ([]ports.Rect, bool)
	if root {
		damage = content.DamageSince
	}
	c, err := r.shmCopyFor(key, st, pixels, b.Offset, b.Stride, damage)
	if err != nil {
		return
	}
	dr := r.contentDraw(rect, full, content.Width, content.Height, content.Source, content.Transform, modeBuffer, content.Opaque)
	r.setContentColor(&dr, content)
	dr.set = c.set
	dr.pc.buf = [4]uint32{0, uint32(content.Width), uint32(content.Height), 0}
	w.draws = append(w.draws, dr)
}

// setContentColor selects only color encodings the protocol accepts, and
// the surface's fade.
func (r *Renderer) setContentColor(dr *draw, content *ports.SurfaceContent) {
	dr.pc.color[3] = 1 - max(0, min(1, content.Fade))
	c := content.Color
	if c.IsPQ2020() {
		dr.pc.misc[1] |= flagPQ
	} else if c.IsExtendedLinear() {
		dr.pc.misc[1] |= flagExtendedLinear
	} else {
		return
	}
	dr.pc.color[0] = float32(r.hdrNits)
	if dr.pc.color[0] <= 0 {
		dr.pc.color[0] = 203
	}
}

func parseColor(s string) [3]uint8 {
	var c [3]uint8
	if len(s) != 7 || s[0] != '#' {
		return c
	}
	for i := range c {
		v, e := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
		if e != nil {
			return [3]uint8{}
		}
		c[i] = uint8(v)
	}
	return c
}
