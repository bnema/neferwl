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
	scale      float64
	bounds     image.Rectangle
	fullscreen bool
	draws      []draw
}

// draws walks the scene into quads in paint order.
func (r *Renderer) draws(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent, dmg *damageRegion) []draw {
	w := &sceneWalk{r: r, s: s, contents: contents, dmg: dmg, scale: s.Scale, bounds: image.Rect(0, 0, r.width, r.height)}
	if w.scale <= 0 {
		w.scale = 1
	}
	for _, win := range s.Windows {
		w.fullscreen = w.fullscreen || (!win.Hidden && win.Fullscreen)
	}
	w.layers(false)
	w.windows()
	w.popups(false)
	w.layers(true)
	w.popups(true)
	return w.draws
}

func (w *sceneWalk) phys(v int) int { return int(math.Round(float64(v) * w.scale)) }

func (w *sceneWalk) physRect(x, y, width, height int) image.Rectangle {
	return image.Rect(w.phys(x), w.phys(y), w.phys(x+width), w.phys(y+height))
}

// fill draws a solid rect (physical pixels).
func (w *sceneWalk) fill(rect image.Rectangle, c [3]uint8) {
	rect = rect.Intersect(w.bounds)
	if rect.Empty() {
		return
	}
	w.draws = append(w.draws, w.r.fillDraw(rect, c))
}

// layers draws the layer surfaces below the windows, or above them.
// A fullscreen window hides the bottom and top layers.
func (w *sceneWalk) layers(afterWindows bool) {
	for _, layer := range w.s.Layers {
		if (layer.Layer >= ports.LayerTop) != afterWindows ||
			(w.fullscreen && (layer.Layer == ports.LayerBottom || layer.Layer == ports.LayerTop)) ||
			layer.Rect.W <= 0 || layer.Rect.H <= 0 {
			continue
		}
		content := w.contents[layer.ID]
		w.dmg.window(layer.ID, content, w.physRect(layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H))
		w.place(layer.ID, &content, layer.Rect.X, layer.Rect.Y, layer.Rect.W, layer.Rect.H)
	}
}

// windows draws the tiled then floating windows with their borders.
// Tile lines (Window 0) go over the tiles, a float's border with it.
func (w *sceneWalk) windows() {
	tileLines := false
	for _, win := range w.s.Windows {
		if win.Floating && !tileLines {
			w.separators(0)
			tileLines = true
		}
		if win.Hidden || win.Popup || win.Rect.W <= 0 || win.Rect.H <= 0 {
			continue
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
		if content.Empty() {
			w.fill(body, windowColor(win.ID))
		} else {
			w.fill(body, parseColor(w.s.Background))
			w.place(win.ID, &content, c.X, c.Y, c.W, c.H)
		}
		if win.Floating {
			w.separators(win.ID)
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
// surface of the tree is keyed by its window and place in it; the tree's
// Seq changes with any of them.
func (w *sceneWalk) place(id ports.WindowID, content *ports.SurfaceContent, x, y, width, height int) {
	clip := image.Rect(x, y, x+width, y+height)
	ox, oy := x-content.Geometry.X, y-content.Geometry.Y
	covers := w.opaqueChildren(content, ox, oy, clip)
	for i := range content.Children {
		if ch := &content.Children[i]; ch.Below {
			w.surface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip, shmKey{id, i + 1}, content.Seq, covers)
		}
	}
	w.surface(content, ox, oy, clip, shmKey{id, 0}, content.Seq, covers)
	for i := range content.Children {
		if ch := &content.Children[i]; !ch.Below {
			w.surface(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip, shmKey{id, i + 1}, content.Seq, nil)
		}
	}
}

// opaqueChildren gives the physical rects shown by the opaque subsurfaces
// above the root (e.g. a game's GPU surface over its window's shm buffer):
// they hide the surfaces below them. A child whose buffer will not draw is
// left out, so the surface below it still shows: a dmabuf that fails to
// import (opaqueChildren pre-imports it) or an unreadable shm buffer.
func (w *sceneWalk) opaqueChildren(content *ports.SurfaceContent, ox, oy int, clip image.Rectangle) []image.Rectangle {
	var covers []image.Rectangle
	for i := range content.Children {
		ch := &content.Children[i]
		if ch.Below || !ch.Opaque {
			continue
		}
		_, dst, ok := w.surfaceRects(&ch.SurfaceContent, ox+ch.X, oy+ch.Y, clip)
		if !ok || !w.drawable(&ch.SurfaceContent) {
			continue
		}
		covers = append(covers, dst)
	}
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
// at (x, y) logical, and the part of it clip (logical) shows. ok is false
// when there is nothing to draw.
func (w *sceneWalk) surfaceRects(content *ports.SurfaceContent, x, y int, clip image.Rectangle) (full, dst image.Rectangle, ok bool) {
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
	full = w.physRect(x, y, lw, lh)
	// A buffer drawn at the physical size (fractional-scale clients round
	// w*scale, we round per edge) is copied 1:1, never resampled for 1px.
	near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
	sourceW, sourceH := content.Width, content.Height
	if content.Source[2] > 0 {
		sourceW, sourceH = int(content.Source[2]), int(content.Source[3])
	}
	if near(full.Dx(), sourceW) && near(full.Dy(), sourceH) {
		full.Max = full.Min.Add(image.Pt(sourceW, sourceH))
	}
	dst = full.Intersect(w.physRect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()))
	return full, dst, !dst.Empty()
}

// surface draws one surface buffer with its origin at (x, y) logical,
// clipped to clip (logical), unless an opaque surface above it (covers,
// physical) hides all of it: its buffer is then neither copied nor drawn.
func (w *sceneWalk) surface(content *ports.SurfaceContent, x, y int, clip image.Rectangle, key shmKey, seq uint64, covers []image.Rectangle) {
	full, dst, ok := w.surfaceRects(content, x, y, clip)
	if !ok {
		return
	}
	for _, c := range covers {
		if dst.In(c) {
			return
		}
	}
	if key.index == 0 {
		w.dmg.content(key.win, content, full, dst)
	}
	w.content(dst, full, content, key, seq)
}

// content draws the part of a buffer mapped onto full (physical pixels)
// that dst shows. key names the surface, seq its content.
func (w *sceneWalk) content(dst, full image.Rectangle, content *ports.SurfaceContent, key shmKey, seq uint64) {
	r := w.r
	rect := dst.Intersect(w.bounds)
	if rect.Empty() {
		return
	}
	if content.DMABuf != nil {
		im, err := r.importDMABuf(content.DMABuf)
		if err != nil {
			// The window shows its background until a buffer imports.
			return
		}
		dr := r.contentDraw(rect, full, content.Width, content.Height, content.Source, modeImage, content.Opaque)
		r.setContentColor(&dr, content.Color)
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
	st := shmState{w: content.Width, h: content.Height, seq: seq}
	// Damage history is the root surface's: children copy in full.
	var damage func(uint64) ([]ports.Rect, bool)
	if key.index == 0 {
		damage = content.DamageSince
	}
	c, err := r.shmCopyFor(key, st, pixels, b.Offset, b.Stride, damage)
	if err != nil {
		return
	}
	dr := r.contentDraw(rect, full, content.Width, content.Height, content.Source, modeBuffer, content.Opaque)
	r.setContentColor(&dr, content.Color)
	dr.set = c.set
	dr.pc.buf = [4]uint32{0, uint32(content.Width), uint32(content.Height), 0}
	w.draws = append(w.draws, dr)
}

// setContentColor selects only color encodings the protocol accepts.
func (r *Renderer) setContentColor(dr *draw, c ports.SurfaceColor) {
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

// windowColor is the placeholder color of a window with no content yet.
func windowColor(id ports.WindowID) [3]uint8 {
	h := math.Mod(float64(id)*0.618034, 1) * 6
	sector := int(h)
	f := h - float64(sector)
	p, q, t := 0.4, 0.8*(1-0.5*f), 0.8*(1-0.5*(1-f))
	var a, b, c float64
	switch sector {
	case 0:
		a, b, c = 0.8, t, p
	case 1:
		a, b, c = q, 0.8, p
	case 2:
		a, b, c = p, 0.8, t
	case 3:
		a, b, c = p, q, 0.8
	case 4:
		a, b, c = t, p, 0.8
	default:
		a, b, c = 0.8, p, q
	}
	return [3]uint8{uint8(a * 255), uint8(b * 255), uint8(c * 255)}
}
