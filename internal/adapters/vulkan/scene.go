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
	// scale maps the scene's logical pixels to scene-physical ones.
	scale float64
	// zoom shrinks the surfaces of the window being placed: an overview
	// preview (ports.SceneWindow.Preview); 1 otherwise.
	zoom float64
	// pulse is the focus effect of the window being placed
	// (ports.SceneWindow.FocusEffect); 0 otherwise.
	pulse float32
	// alpha is the opacity of the window being drawn, 1 - its Fade
	// (ports.SceneWindow.Fade): its fills, lines and surfaces are scaled by
	// it (premultiplied); 1 otherwise.
	alpha  float32
	bounds image.Rectangle
	// tileBounds is bounds cut to Scene.TileClip: where tiles draw.
	tileBounds image.Rectangle
	draws      []draw
}

// draws walks the scene into quads in paint order.
func (r *Renderer) draws(s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent, dmg *damageRegion) []draw {
	clear(r.scratchDraws) // release references from longer earlier scenes
	// The walk stays in scene-physical space (the target's size, swapped for
	// a rotated output); orient maps the draws to the target afterwards.
	sw, sh := r.sceneSize(s.Transform)
	w := &sceneWalk{r: r, s: s, contents: contents, dmg: dmg, scale: s.Scale, zoom: 1, alpha: 1, bounds: image.Rect(0, 0, sw, sh), draws: r.scratchDraws[:0]}
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
	w.tileBounds = w.bounds
	if s.TileClip != (ports.Rect{}) {
		c := s.TileClip
		w.tileBounds = w.bounds.Intersect(w.physRect(c.X, c.Y, c.W, c.H))
	}
	w.windows()
	w.popups(false)
	w.dropHints()
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

// dropHints fills where a dragged tile would land (ports.Scene.DropHints),
// in the active border color, over the workspace's windows.
func (w *sceneWalk) dropHints() {
	if len(w.s.DropHints) == 0 {
		return
	}
	col := parseColor(w.s.Border.Active)
	for _, r := range w.s.DropHints {
		if r.W > 0 && r.H > 0 {
			w.fill(w.physRect(r.X, r.Y, r.W, r.H), col)
		}
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

// fill draws a solid rect (physical pixels), at the walk's alpha.
func (w *sceneWalk) fill(rect image.Rectangle, c [3]uint8) {
	rect = rect.Intersect(w.bounds)
	if rect.Empty() {
		return
	}
	dr := w.r.fillDraw(rect, c)
	if w.alpha < 1 {
		w.r.fadeSolid(&dr, w.alpha)
	}
	w.draws = append(w.draws, dr)
}

// dim paints a translucent black quad over rect (physical), clipped to the
// output, at alpha scaled by the walk's alpha. Damage clipping later limits
// it to the region being repainted, just like other fills.
func (w *sceneWalk) dim(rect image.Rectangle, alpha float64) {
	if rect = rect.Intersect(w.bounds); !rect.Empty() {
		w.draws = append(w.draws, w.r.dimDraw(rect, alpha*float64(w.alpha)))
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

// drawsAsWindow reports whether windows() paints win as a window (not
// hidden, a popup or empty).
func drawsAsWindow(win ports.SceneWindow) bool {
	return !win.Hidden && !win.Popup && win.Rect.W > 0 && win.Rect.H > 0
}

// opensFloats reports whether win, once reached, closes the tiles: the tile
// lines and the dim veil are painted under it (an overview preview of a
// float is not one).
func opensFloats(win ports.SceneWindow) bool {
	return !win.Below && win.Floating && win.Preview == 0
}

// floatsStart is the index of the first window that opens the floats in s,
// or -1 when none does: where windows() splits its paint order.
func floatsStart(s ports.Scene) int {
	for i, win := range s.Windows {
		if drawsAsWindow(win) && opensFloats(win) {
			return i
		}
	}
	return -1
}

// windows draws ordered placements with borders. Tile lines go after
// the column group, before the first float above it; an overview preview
// of a float is not one. The veil goes under that float, or under every
// window with DimBehind.
func (w *sceneWalk) windows() {
	if w.s.DimBehind && w.s.Dim > 0 {
		w.dim(w.bounds, w.s.Dim)
	}
	tileLines := false
	for _, win := range w.s.Windows {
		if !drawsAsWindow(win) {
			continue
		}
		if opensFloats(win) && !tileLines {
			w.separators(0)
			tileLines = true
			if w.s.Dim > 0 && !w.s.DimBehind {
				w.dim(w.bounds, w.s.Dim)
			}
		}
		if win.Fade >= 1 {
			// Faded out: it still opens the floats (its lines and veil are
			// painted under where it would be), but draws nothing.
			continue
		}
		w.alpha = 1 - float32(max(0, win.Fade))
		windowBounds := w.bounds
		if win.Tile() {
			// A tile never draws over a panel (Scene.TileClip).
			w.bounds = w.tileBounds
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
			// An animating frame zooms its content (Zoom); a card draws
			// at its Preview.
			if win.Zoom > 0 {
				w.zoom = win.Zoom
			} else if win.Preview > 0 {
				w.zoom = win.Preview
			}
			w.pulse = float32(max(0, min(win.FocusEffect, 1)))
			w.place(win.ID, &content, c.X, c.Y, c.W, c.H)
			w.zoom, w.pulse = 1, 0
		}
		if win.Floating {
			w.separators(win.ID)
		}
		if win.Dim > 0 {
			// A stashed window peeking in: dimmed with its border.
			w.dim(w.physRect(win.Rect.X, win.Rect.Y, win.Rect.W, win.Rect.H), win.Dim)
		}
		w.alpha, w.bounds = 1, windowBounds
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

// opaqueChildren gives the physical rects shown opaque by the subsurfaces
// above the root (e.g. a game's GPU surface over its window's shm buffer):
// they hide the surfaces below them. A child that is not wholly opaque but
// has an opaque region (Firefox's page, whose region stops a pixel short of
// its edges) hides the surfaces below only inside that rect. A child whose
// buffer will not draw is left out, so the surface below it still shows: a
// dmabuf that fails to import (opaqueChildren pre-imports it) or an
// unreadable shm buffer.
func (w *sceneWalk) opaqueChildren(content *ports.SurfaceContent, ox, oy float64, clip image.Rectangle) []image.Rectangle {
	covers := w.r.scratchCovers[:0]
	for i := range content.Children {
		ch := &content.Children[i]
		if ch.Below || !ch.Opaque && ch.OpaqueRect.W <= 0 {
			continue
		}
		full, dst, ok := w.surfaceRects(&ch.SurfaceContent, ox+float64(ch.X)*w.zoom, oy+float64(ch.Y)*w.zoom, clip)
		if !ok || !w.drawable(&ch.SurfaceContent) {
			continue
		}
		if !ch.Opaque {
			if dst = opaqueCover(&ch.SurfaceContent, full, dst); dst.Empty() {
				continue
			}
		}
		covers = append(covers, dst)
	}
	w.r.scratchCovers = covers
	return covers
}

// opaqueCover is the part of dst (physical, what the surface shows of full)
// that its OpaqueRect maps to. Edges round inward, so every pixel of it is
// inside the region; an edge on the surface's own edge stays on dst's.
func opaqueCover(c *ports.SurfaceContent, full, dst image.Rectangle) image.Rectangle {
	lw, lh := c.LogicalW, c.LogicalH
	if lw <= 0 || lh <= 0 {
		lw, lh = c.Width, c.Height
	}
	o := c.OpaqueRect
	if lw <= 0 || lh <= 0 || o.W <= 0 || o.H <= 0 {
		return image.Rectangle{}
	}
	// ceil and floor of full.Min + v*size/n, v >= 0.
	lo := func(min, v, size, n int) int { return min + (v*size+n-1)/n }
	hi := func(min, v, size, n int) int { return min + v*size/n }
	r := image.Rect(lo(full.Min.X, max(o.X, 0), full.Dx(), lw), lo(full.Min.Y, max(o.Y, 0), full.Dy(), lh),
		hi(full.Min.X, min(o.X+o.W, lw), full.Dx(), lw), hi(full.Min.Y, min(o.Y+o.H, lh), full.Dy(), lh))
	return r.Intersect(dst)
}

// drawable reports whether the child's buffer will draw. GPU buffer
// imports are cached, so pre-importing here costs nothing.
func (w *sceneWalk) drawable(c *ports.SurfaceContent) bool {
	if c.Solid != nil {
		return true
	}
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
	if !content.HasBuffer() || lw <= 0 || lh <= 0 || content.Width <= 0 || content.Height <= 0 {
		return full, dst, false
	}
	if content.SHM != nil && (content.SHM.Stride < content.Width*4 || content.SHM.Offset < 0) {
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
	sourceW, sourceH = content.Transform.Size(sourceW, sourceH)
	if near(full.Dx(), sourceW) && near(full.Dy(), sourceH) {
		full.Max = full.Min.Add(image.Pt(sourceW, sourceH))
	}
	dst = full.Intersect(w.physRect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy())).Intersect(w.bounds)
	return full, dst, !dst.Empty()
}

// surface draws one surface buffer with its origin at (x, y) logical,
// clipped to clip (logical), unless an opaque surface above it (covers,
// physical) hides all of it: its buffer is then neither copied nor drawn.
// A cover over only part of it clips the draw to the rest.
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
	n := len(w.draws)
	w.content(dst, full, content, key, seq, root)
	if len(w.draws) == n+1 && len(covers) > 0 {
		// The quad's mapping comes from full: only its rect shrinks.
		d := w.draws[n]
		w.draws = w.draws[:n]
		w.split(d, dst, covers)
	}
}

// Splitting a quad around covers is bounded: covers come from the client, and
// each can cut a quad into four strips, recursively. Past these bounds the
// quad is drawn whole: the covers above it hide the rest, at the cost of
// overdraw.
const (
	maxSplitCovers = 8  // covers crossing the quad that are still split around
	maxSplitStrips = 32 // draws one quad may become
)

// split appends d, a quad over rect, minus the covers, or the whole quad
// when the covers cut it into too many strips.
func (w *sceneWalk) split(d draw, rect image.Rectangle, covers []image.Rectangle) {
	crossing := 0
	for _, c := range covers {
		if !rect.Intersect(c).Empty() {
			crossing++
		}
	}
	n := len(w.draws)
	if crossing <= maxSplitCovers {
		w.uncovered(d, rect, covers, n+maxSplitStrips)
		if len(w.draws) <= n+maxSplitStrips {
			return
		}
		w.draws = w.draws[:n]
	}
	w.quad(d, rect)
}

// uncovered appends d, a quad over rect, minus the covers: the strips of
// rect around each cover, in turn. d keeps its mapping. It stops once the
// draws pass limit, for the caller to drop them.
func (w *sceneWalk) uncovered(d draw, rect image.Rectangle, covers []image.Rectangle, limit int) {
	if len(w.draws) > limit {
		return
	}
	for i, c := range covers {
		in := rect.Intersect(c)
		if in.Empty() {
			continue
		}
		rest := covers[i+1:]
		w.uncovered(d, image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, in.Min.Y), rest, limit)
		w.uncovered(d, image.Rect(rect.Min.X, in.Max.Y, rect.Max.X, rect.Max.Y), rest, limit)
		w.uncovered(d, image.Rect(rect.Min.X, in.Min.Y, in.Min.X, in.Max.Y), rest, limit)
		w.uncovered(d, image.Rect(in.Max.X, in.Min.Y, rect.Max.X, in.Max.Y), rest, limit)
		return
	}
	w.quad(d, rect)
}

// quad appends d over rect, unless it is empty.
func (w *sceneWalk) quad(d draw, rect image.Rectangle) {
	if rect.Empty() {
		return
	}
	d.pc.rect = [4]int32{int32(rect.Min.X), int32(rect.Min.Y), int32(rect.Max.X), int32(rect.Max.Y)}
	w.draws = append(w.draws, d)
}

// content draws the part of a buffer mapped onto full (physical pixels)
// that dst shows. key names the surface, seq its content.
func (w *sceneWalk) content(dst, full image.Rectangle, content *ports.SurfaceContent, key shmKey, seq uint64, root bool) {
	r := w.r
	rect := dst
	if rect.Empty() {
		return
	}
	if content.Solid != nil {
		dr := r.solidDraw(rect, content)
		if w.alpha < 1 {
			r.fadeSolid(&dr, w.alpha)
		}
		if dr.pc.color[3] > 0 {
			w.draws = append(w.draws, dr)
		}
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
		dr.pc.color[3] *= w.alpha
		dr.pc.mapy[2] = w.pulse
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
	st := shmState{w: content.Width, h: content.Height, seq: seq, windowSeq: content.Seq}
	// The copy of this content, if any, needs no pool: a kept (closing)
	// window whose pool file is closed still draws.
	c := r.shmCached(key, st)
	if c != nil {
		// As a drawn pool, for Trim; nothing is mapped for it.
		if m := r.pools[b.Pool]; m != nil {
			m.last = r.frame
		}
	} else {
		pixels, err := r.shmPixels(b, b.Offset+(content.Height-1)*b.Stride+content.Width*4)
		if err != nil {
			return
		}
		// Damage history is the root surface's: children copy in full.
		var damage func(uint64) ([]ports.Rect, bool)
		if root {
			damage = content.DamageSince
		}
		if c, err = r.shmCopyFor(key, st, pixels, b.Offset, b.Stride, damage); err != nil {
			return
		}
	}
	dr := r.contentDraw(rect, full, content.Width, content.Height, content.Source, content.Transform, modeBuffer, content.Opaque)
	r.setContentColor(&dr, content)
	dr.pc.color[3] *= w.alpha
	dr.pc.mapy[2] = w.pulse
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
