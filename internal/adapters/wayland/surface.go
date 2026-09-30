package wayland

import (
	"image"
	"math"
	"os"
	"slices"
	"time"

	"github.com/bnema/purego-libwayland/protocol/presentationtime"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
)

type roleKind uint8

const (
	roleNone roleKind = iota
	roleXDG
	roleSubsurface
	roleLayer
	roleCursor
	roleInputPopup
	roleSessionLock
)

type surface struct {
	wl                *wayland.Surface
	viewport          *viewport
	committedViewport viewportState // value snapshot; no allocation on viewport commits
	// next is the state requested since the last commit (see pendingCommit).
	next pendingCommit
	// bufferScale, transform and opaque are committed state. formatOpaque is
	// the current buffer's own opacity (x formats).
	bufferScale  int
	transform    ports.BufferTransform
	opaque       []ports.Rect
	formatOpaque bool
	kind         roleKind
	xdg          *xdgSurface
	layer        *layerSurface
	lock         *lockSurface
	// bufferCommitted is permanent, including commits later discarded by FIFO.
	bufferCommitted bool
	server          *Server
	current         *wayland.Buffer
	lastW, lastH    int // last logged buffer size
	role            func(bool)
	destroyed       bool
	// hotX, hotY is the click point of a cursor surface (logical).
	hotX, hotY int
	// on is the output the surface entered; scale is the last scale sent.
	on    *output
	scale float64
	// content is the last content built from this surface's own buffer;
	// has is false while no buffer is attached.
	inputAll                bool
	inputRects              []ports.Rect
	sentInput, lastInputAll bool
	lastInputRects          []ports.Rect
	content                 ports.SurfaceContent
	has                     bool
	identity                uint64
	version                 uint64
	// cachedTree is never modified once returned to emitContent.
	cachedTree                 []ports.Subsurface
	treeDirty                  bool
	commitFresh, commitSkipped bool
	queuedScale                int
	queuedBuffer               uint32
	sub                        subState
	// tearing is the surface's wp_tearing_control_v1; async the committed
	// hint.
	tearing               *tearingHandler
	async                 bool
	colorControl          *colorSurface
	color                 SurfaceColor
	representation        surfaceRepresentation
	representationControl *representationSurface
	// Presentation constraints (fifo.go): the barrier, and the commits
	// waiting to apply.
	fifo      *fifoHandler
	timer     *timerHandler
	barrier   bool
	barrierAt time.Time
	queue     []*update
	// contentType is the wp_content_type_v1; contentKind the committed type.
	contentType *contentTypeHandler
	// alpha is the surface's wp_alpha_modifier_surface_v1.
	alpha       *alphaHandler
	contentKind uint32
	// committed is what the last commit changed, in buffer pixels (full:
	// everything), read by the window's damage history.
	committed damage
	// Explicit sync (syncobj.go): the surface's syncobj object and the
	// current buffer's hold.
	sync *syncState
	hold syncHold
}

// pendingCommit is the double-buffered wl_surface state requested since the
// last commit. A commit captures it whole into an update (takePending):
// one-shot parts are cleared, sticky parts (scale, transform, hints, color)
// carry over to the next commit.
type pendingCommit struct {
	// buffer is attached (attached reports an attach request, nil detaches).
	buffer   *wayland.Buffer
	attached bool
	// scale is set by set_buffer_scale, transform by set_buffer_transform.
	scale     int
	transform ports.BufferTransform
	// opaque and the input region are set by their requests (the Set flags).
	opaque             []ports.Rect
	opaqueSet          bool
	inputAll, inputSet bool
	inputRects         []ports.Rect
	callbacks          []*wayland.Callback
	async              bool
	color              SurfaceColor
	representation     surfaceRepresentation
	kind               uint32
	// fade is 1 - the wp_alpha_modifier_v1 multiplier (sticky).
	fade float32
	// barrier and wait are fifo requests; at the commit timestamp.
	barrier, wait bool
	at            time.Time
	// damage is in surface (logical) pixels, bufDamage in buffer pixels.
	damage, bufDamage []ports.Rect
	// feedback are wp_presentation feedbacks for this commit.
	feedback []*presentationtime.WpPresentationFeedback
	// sync is the commit's explicit-sync points, with the wait for its
	// acquire point.
	sync *commitSync
}

// commitSync is a commit's explicit-sync points and acquire wait.
type commitSync struct {
	acquire, release syncPoint
	wait             *syncWait
	fence            *os.File // exported before application; owned until apply/drop
}

// damage is what one commit changed in a surface's buffer.
type damage struct {
	full  bool
	rects []ports.Rect
}

// maxDamageRects bounds one commit's rects: past it the commit is full.
const maxDamageRects = 32

// subState is the subsurface tree: parent is set on subsurfaces, children
// are ordered bottom to top, below marks children under their parent.
type subState struct {
	// role is the live wl_subsurface, nil when the surface has none.
	role                  *wayland.Subsurface
	parent                *surface
	children              []*surface // structural children
	pendingLayout, layout []childLayout
	synced                bool // explicit synchronization mode
}

// root is the top of the surface's subsurface tree.
func (s *surface) root() *surface {
	for s.sub.parent != nil {
		s = s.sub.parent
	}
	return s
}

// windowID is the mapped window or layer the surface draws, or 0.
func (s *surface) windowID() ports.WindowID {
	if s.xdg != nil && s.xdg.window != nil && s.xdg.window.mapped {
		return s.xdg.window.id
	}
	if s.layer != nil && s.layer.mapped {
		return s.layer.id
	}
	if s.lock != nil && s.lock.mapped {
		return s.lock.id
	}
	return 0
}

// outputColor converts the committed protocol description without allocating.
func (s *surface) outputColor() ports.SurfaceColor {
	return ports.SurfaceColor{TF: uint8(s.color.TF), Primaries: uint8(s.color.Primaries), MaxCLL: uint16(s.color.MaxCLL), MaxFALL: uint16(s.color.MaxFALL), Coefficients: s.representation.coefficients, Range: s.representation.rangeValue, Chroma: s.representation.chroma}
}

func (s *surface) contentWithColor() ports.SurfaceContent {
	c := s.content
	c.Surface, c.Version = s.identity, s.version
	c.Color = s.outputColor()
	// A subsurface asks for tearing itself: the output may scan it out.
	c.Async = s.async
	return c
}

// tree is the root's content with its subsurfaces flattened.
func (s *surface) tree(id ports.WindowID) ports.SurfaceContent {
	c := s.contentWithColor()
	c.ID = id
	c.ContentType = ports.ContentType(s.contentKind)
	if s.treeDirty || s.cachedTree == nil {
		count := s.treeCount()
		children := make([]ports.Subsurface, 0, count)
		for _, item := range s.sub.layout {
			item.child.appendTree(&children, item.x, item.y, item.below)
		}
		s.cachedTree, s.treeDirty = children, false
	}
	c.Children = s.cachedTree
	if s.xdg != nil {
		c.Geometry = s.xdg.geometry
	}
	return c
}

func (s *surface) treeCount() int {
	n := 0
	for _, item := range s.sub.layout {
		if item.child.has {
			n++
		}
		n += item.child.treeCount()
	}
	return n
}

// appendTree flattens a subsurface at (x, y) from the root: its children
// below it, itself, then its children above.
func (s *surface) appendTree(out *[]ports.Subsurface, x, y int, below bool) {
	for _, item := range s.sub.layout {
		if item.below {
			item.child.appendTree(out, x+item.x, y+item.y, below)
		}
	}
	if s.has {
		*out = append(*out, ports.Subsurface{X: x, Y: y, Below: below, SurfaceContent: s.contentWithColor()})
	}
	for _, item := range s.sub.layout {
		if !item.below {
			item.child.appendTree(out, x+item.x, y+item.y, below)
		}
	}
}

// redraw sends the tree of a mapped root to the outputs. Only a commit
// of the root's own buffer with no other change keeps partial damage.
func (s *surface) redraw() {
	r := s.root()
	if id := r.windowID(); id != 0 && s.server.channels.Contents != nil {
		d := damage{full: true}
		if s == r && len(r.sub.children) == 0 {
			d = r.committed
		}
		s.server.emitContent(r.tree(id), d)
	}
}

// detach removes a subsurface from its parent and redraws the parent.
func (s *surface) detach() {
	p := s.sub.parent
	if p == nil {
		return
	}
	for i, ch := range p.sub.children {
		if ch == s {
			p.sub.children = append(p.sub.children[:i:i], p.sub.children[i+1:]...)
			break
		}
	}
	p.sub.pendingLayout = removeLayout(p.sub.pendingLayout, s)
	p.sub.layout = removeLayout(p.sub.layout, s)
	p.root().treeDirty = true
	for _, u := range p.queue {
		u.layout = removeLayout(u.layout, s)
	}
	s.sub.parent = nil
	// Parent updates cannot make a detached child's bound commits visible.
	s.dropQueue()
	s.flushDesync()
	p.redraw()
	p.emitInput()
	s.sendScale()
}

func (s *surface) Destroy(*wayland.Surface) {
	s.destroyed = true
	for _, fb := range s.next.feedback {
		discard(fb)
	}
	s.next.feedback = nil
	s.tearing = nil // the control becomes inert
	s.colorControl = nil
	s.representationControl = nil
	s.dropQueue()
	if s.server.cursorSurface == s {
		// The pointer keeps no cursor until the client sets another.
		s.server.cursorSurface = nil
		s.server.setCursor(ports.CursorChange{Hidden: true})
	}
	s.detach()
	for _, ch := range s.sub.children {
		ch.sub.parent = nil
		ch.dropQueue()
		ch.flushDesync()
	}
	s.sub.children = nil
	s.sub.layout, s.sub.pendingLayout = nil, nil
	for _, cb := range s.next.callbacks {
		cb.Destroy()
	}
	s.next.callbacks = nil
	// The client may reuse the buffer on another surface.
	s.dropSync(s.next.sync)
	s.next.sync = nil
	if s.current != nil {
		s.server.releaseBuffer(s, s.current, s.hold)
		s.hold = syncHold{}
	}
	s.current, s.next.buffer = nil, nil
	if s.role != nil {
		s.role(false)
	}
	if c := s.server.constraints[s]; c != nil {
		s.server.dropConstraint(c)
	}
}
func (s *surface) Attach(_ *wayland.Surface, b *wayland.Buffer, _, _ int32) {
	s.next.buffer = b
	s.next.attached = true
}
func (s *surface) Frame(r *wayland.Surface, id uint32) {
	cb, err := wayland.NewCallback(r.Client(), 1, id, struct{}{})
	if err == nil {
		s.next.callbacks = append(s.next.callbacks, cb)
	}
}
func (s *surface) Commit(*wayland.Surface) {
	if s.next.attached && s.next.buffer != nil {
		s.bufferCommitted = true
	}
	if s.lock != nil && !s.lock.checkCommit() {
		return
	}
	if s.layer != nil && s.next.attached && s.next.buffer != nil && !s.layer.acked {
		s.layer.resource.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState), "buffer before configure ack")
		return
	}
	if s.xdg != nil && s.next.attached && s.next.buffer != nil && !s.xdg.acked {
		s.xdg.resource.PostError(uint32(xdgshell.SurfaceErrorUnconfiguredBuffer), "buffer before initial configure ack")
		return
	}
	if !s.checkSyncCommit() || !s.checkRepresentationCommit() {
		return
	}
	s.takeSyncPoints()
	// Each request creates an ordered content update. A synchronized update
	// can only be reached through a parent's committed dependency graph.
	s.queueUpdate()
	s.server.tickFifo(time.Now(), nil)
}

// applyCommit makes only the captured update current. Live pending requests
// remain untouched, even when this update waited behind another commit.
func (s *surface) applyCommit(u *update) {
	oldW, oldH, oldSource, oldTransform := s.content.LogicalW, s.content.LogicalH, s.content.Source, s.content.Transform
	oldColor, oldRepresentation := s.color, s.representation
	fresh := u.attached && u.buffer != nil
	s.commitFresh = fresh
	if !s.commitSkipped {
		s.queuedScale = u.scale
		s.queuedBuffer = 0
		if u.buffer != nil {
			s.queuedBuffer = u.buffer.ID()
		}
	}
	if u.barrier {
		s.setBarrier(time.Now())
	}
	if s.contentKind != u.kind {
		s.contentKind = u.kind
		s.server.log.Info().Str("component", "wayland").Uint64("id", uint64(s.root().windowID())).Uint32("content_type", s.contentKind).Msg("content type")
	}
	if u.scale > 0 {
		s.bufferScale = u.scale
	}
	s.transform = u.transform
	oldOpaque := s.content.Opaque
	if u.opaqueSet {
		s.opaque = u.opaque
	}
	oldFade := s.content.Fade
	hinted := s.async != u.async || s.color != u.color || s.representation != u.representation
	if s.async != u.async {
		// Contents carry the hint of every surface in the tree.
		s.root().treeDirty = true
	}
	s.async = u.async
	s.color = u.color
	s.representation = u.representation
	if s.lock != nil && s.commitSkipped {
		// A discarded replacement cannot change retained lock geometry, even
		// if its viewport was destroyed before capture or before application.
	} else if u.vp != nil && (s.lock != nil || u.vp.resource != nil && u.vp.resource.Resource.Alive()) {
		s.committedViewport.destW, s.committedViewport.destH, s.committedViewport.dest = u.vpW, u.vpH, u.vpSet
		s.committedViewport.src, s.committedViewport.crop = u.vpSrc, u.vpCrop
	} else {
		s.committedViewport.dest, s.committedViewport.crop = false, false
	}
	cs := u.sync
	if u.attached {
		if s.current != nil && (u.buffer == nil || s.current.Resource != u.buffer.Resource) {
			s.server.releaseBuffer(s, s.current, s.hold)
		} else if cs != nil || u.buffer == nil {
			// Same buffer again, or detached: the old points are done
			// with once the new ones take over.
			s.server.releaseBufferSync(s, s.hold)
		}
		s.hold = syncHold{}
		s.current = u.buffer
	}
	s.applySync(cs)
	if u.inputSet {
		s.inputAll, s.inputRects = u.inputAll, u.inputRects
	}
	if len(u.callbacks) > 0 {
		s.server.queueFrames(s, u.callbacks)
	}
	if s.lock != nil && !s.commitSkipped && !s.lock.checkCaptured(u) {
		return
	}
	if s.role != nil {
		if u.layer != nil && s.layer == u.layer {
			u.layer.commitState(u.layerNext, s.current != nil)
		} else {
			s.role(s.current != nil)
		}
	}
	if fresh {
		if state, ok := s.server.buffers[s.current.Resource]; ok {
			if c, ok := state.content(0); ok {
				if !s.validateViewport(c.Width, c.Height) {
					return
				}
				c.Transform = s.transform
				c.Source, _ = s.source(c.Width, c.Height)
				c.LogicalW, c.LogicalH = s.logicalSize(c.Width, c.Height)
				s.formatOpaque = c.Opaque
				c.Opaque = (c.Opaque || s.opaqueCovers(c.LogicalW, c.LogicalH)) && u.fade == 0
				c.Fade = u.fade
				resized := !s.has || c.Width != s.content.Width || c.Height != s.content.Height || c.LogicalW != s.content.LogicalW || c.LogicalH != s.content.LogicalH
				s.commitDamage(u, true, resized, c.Width, c.Height)
				if s.sub.parent == nil && (c.Width != s.lastW || c.Height != s.lastH) {
					s.lastW, s.lastH = c.Width, c.Height
					s.server.log.Info().Uint64("id", uint64(s.windowID())).Int("w", c.Width).Int("h", c.Height).Msg("buffer size")
				}
				c.Acquire = s.hold.acquire
				s.content, s.has = c, true
			} else if shm, ok := state.(*buffer); ok {
				shm.pool.shm.PostError(uint32(wayland.ShmErrorInvalidFd), "SHM backing file truncated")
			}
			// Buffers are read in place: released when the next one
			// replaces them.
		}
	}
	damaged := !fresh && s.current != nil && (len(u.damage) > 0 || len(u.bufDamage) > 0)
	if !fresh {
		s.commitDamage(u, false, false, 0, 0)
		// A NULL attach is exempt from out_of_buffer: its content is
		// cleared below, so the retained buffer is not validated.
		if s.has && s.current != nil {
			if !s.validateViewport(s.content.Width, s.content.Height) {
				return
			}
			s.content.Transform = s.transform
			s.content.Source, _ = s.source(s.content.Width, s.content.Height)
			s.content.LogicalW, s.content.LogicalH = s.logicalSize(s.content.Width, s.content.Height)
			s.content.Opaque = (s.formatOpaque || s.opaqueCovers(s.content.LogicalW, s.content.LogicalH)) && u.fade == 0
			s.content.Fade = u.fade
		}
	}
	if s.current == nil {
		s.content, s.has = ports.SurfaceContent{}, false
	}
	if fresh || damaged || oldW != s.content.LogicalW || oldH != s.content.LogicalH || oldSource != s.content.Source || oldColor != s.color || oldRepresentation != s.representation || oldTransform != s.content.Transform || oldOpaque != s.content.Opaque || oldFade != s.content.Fade {
		s.version++
		s.root().treeDirty = true
	}
	// The layout belongs to this update, not to the latest requests.
	moved := !sameLayout(s.sub.layout, u.layout)
	if moved {
		s.root().treeDirty = true
	}
	// Queued updates can still reference the previous layout. Never mutate it.
	if moved {
		s.sub.layout = u.layout
	}
	geometry := false
	if s.xdg != nil && s.xdg == u.xdg && u.geometry != s.xdg.geometry {
		s.xdg.geometry, geometry = u.geometry, true
	}
	s.commitConstraint(u.cons, u.region, geometry)
	s.emitInput()
	if s.xdg != nil && s.xdg.window != nil {
		s.xdg.window.afterCommit()
	}
	if s.lock != nil {
		// Role callbacks precede content construction; publish lock placement
		// only once validated current dimensions are available.
		s.server.lockSurfaceChanged()
	}
	reshaped := s.has && (s.content.LogicalW != oldW || s.content.LogicalH != oldH || s.content.Source != oldSource || s.content.Transform != oldTransform)
	// Opacity changes how every pixel blends, so it repaints the whole surface.
	opacity := s.has && (s.content.Opaque != oldOpaque || s.content.Fade != oldFade)
	drawn := fresh || damaged || moved || geometry || hinted || reshaped || opacity || s.sub.parent != nil
	if drawn {
		if moved || geometry || reshaped || opacity {
			s.committed = damage{full: true}
		}
		if s.server.applyingGraph {
			s.server.graphDrawn = true
		} else {
			s.redraw()
		}
	}
	fb := u.feedback
	if s.server.applyingGraph {
		s.server.graphFeedback = append(s.server.graphFeedback, graphFeedback{s, fb, fresh})
	} else {
		s.commitFeedback(fb, fresh)
	}
}
func (s *surface) Damage(_ *wayland.Surface, x, y, w, h int32) {
	if w > 0 && h > 0 && len(s.next.damage) <= maxDamageRects {
		s.next.damage = append(s.next.damage, ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)})
	}
}
func (s *surface) DamageBuffer(_ *wayland.Surface, x, y, w, h int32) {
	if w > 0 && h > 0 && len(s.next.bufDamage) <= maxDamageRects {
		s.next.bufDamage = append(s.next.bufDamage, ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)})
	}
}

// commitDamage turns the requested damage into buffer pixels of a bw×bh
// buffer. Surface damage scales by the buffer/logical ratio, rounded out.
// A new size, too many rects or none at all with a new buffer (clients
// that do not report damage) is a full change.
func (s *surface) commitDamage(u *update, fresh, resized bool, bw, bh int) {
	surf, buf := u.damage, u.bufDamage
	if !fresh {
		s.committed = damage{}
		return
	}
	if resized || len(surf)+len(buf) == 0 || len(surf)+len(buf) > maxDamageRects {
		s.committed = damage{full: true}
		return
	}
	lw, lh := s.logicalSize(bw, bh)
	d := damage{rects: make([]ports.Rect, 0, len(surf)+len(buf))}
	bounds := image.Rect(0, 0, bw, bh)
	add := func(r image.Rectangle) {
		if r = r.Intersect(bounds); !r.Empty() {
			d.rects = append(d.rects, ports.Rect{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()})
		}
	}
	for _, r := range buf {
		add(image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H))
	}
	// Surface damage maps through the crop, in the transformed buffer's
	// axes, then back through the buffer transform.
	tw, th := s.transformedSize(bw, bh)
	sx, sy, sw, sh := 0.0, 0.0, float64(tw), float64(th)
	if s.committedViewport.crop {
		v := s.committedViewport.src
		scale := float64(max(s.bufferScale, 1)) / 256
		sx, sy, sw, sh = float64(v[0])*scale, float64(v[1])*scale, float64(v[2])*scale, float64(v[3])*scale
	}
	for _, r := range surf {
		if lw <= 0 || lh <= 0 {
			s.committed = damage{full: true}
			return
		}
		// Clients may damage INT32_MAX-sized rects: clamp before scaling.
		x0, y0 := max(r.X, 0), max(r.Y, 0)
		x1, y1 := min(r.X+min(r.W, lw), lw), min(r.Y+min(r.H, lh), lh)
		ax, ay := s.transform.ToBuffer(sx+float64(x0)*sw/float64(lw), sy+float64(y0)*sh/float64(lh), float64(tw), float64(th))
		bx, by := s.transform.ToBuffer(sx+float64(x1)*sw/float64(lw), sy+float64(y1)*sh/float64(lh), float64(tw), float64(th))
		add(image.Rect(int(math.Floor(min(ax, bx))), int(math.Floor(min(ay, by))), int(math.Ceil(max(ax, bx))), int(math.Ceil(max(ay, by)))))
	}
	s.committed = d
}

// SetOpaqueRegion copies the region: a nil region means nothing is opaque.
func (s *surface) SetOpaqueRegion(_ *wayland.Surface, reg *wayland.Region) {
	s.next.opaqueSet = true
	s.next.opaque = nil
	if reg != nil && reg.Resource != nil {
		if g := s.server.regions[reg.Resource]; g != nil {
			s.next.opaque = slices.Clone(g.rects)
		}
	}
}

func (s *surface) SetBufferTransform(r *wayland.Surface, v int32) {
	if v < 0 || v > 7 {
		r.PostError(uint32(wayland.SurfaceErrorInvalidTransform), "invalid buffer transform")
		return
	}
	s.next.transform = ports.BufferTransform(v)
}

// opaqueCovers reports whether the committed opaque region covers the whole
// w×h surface. A partial region is ignored: it is only an optimization hint.
func (s *surface) opaqueCovers(w, h int) bool {
	return w > 0 && h > 0 && covers(s.opaque, ports.Rect{W: w, H: h})
}

// maxCoverRects bounds the region covers checks: beyond it, not covered.
const maxCoverRects = 16

// covers reports whether the union of rects contains all of r. It tests
// one point per cell of the grid the rect edges draw.
func covers(rects []ports.Rect, r ports.Rect) bool {
	if len(rects) == 0 || len(rects) > maxCoverRects {
		return false
	}
	var xsBuf, ysBuf [2*maxCoverRects + 2]int
	xs, ys := append(xsBuf[:0], r.X, r.X+r.W), append(ysBuf[:0], r.Y, r.Y+r.H)
	for _, c := range rects {
		if c.X > r.X && c.X < r.X+r.W {
			xs = append(xs, c.X)
		}
		if e := c.X + c.W; e > r.X && e < r.X+r.W {
			xs = append(xs, e)
		}
		if c.Y > r.Y && c.Y < r.Y+r.H {
			ys = append(ys, c.Y)
		}
		if e := c.Y + c.H; e > r.Y && e < r.Y+r.H {
			ys = append(ys, e)
		}
	}
	slices.Sort(xs)
	slices.Sort(ys)
	for i := 1; i < len(xs); i++ {
		for j := 1; j < len(ys); j++ {
			if xs[i] == xs[i-1] || ys[j] == ys[j-1] {
				continue
			}
			x, y := xs[i-1], ys[j-1]
			if !slices.ContainsFunc(rects, func(c ports.Rect) bool {
				return x >= c.X && x < c.X+c.W && y >= c.Y && y < c.Y+c.H
			}) {
				return false
			}
		}
	}
	return true
}
func (s *surface) SetBufferScale(r *wayland.Surface, v int32) {
	if v < 1 {
		r.PostError(uint32(wayland.SurfaceErrorInvalidScale), "buffer scale must be positive")
		return
	}
	s.next.scale = int(v)
}
func (*surface) Offset(*wayland.Surface, int32, int32) {}
func (*surface) GetRelease(*wayland.Surface, uint32)   {}
