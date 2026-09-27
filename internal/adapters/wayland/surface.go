package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"image"
	"math"
	"time"

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
)

type surface struct {
	wl                *wayland.Surface
	viewport          *viewport
	committedViewport *viewport
	// bufferScale is committed state; pendingScale is set by set_buffer_scale.
	bufferScale, pendingScale int
	kind                      roleKind
	xdg                       *xdgSurface
	layer                     *layerSurface
	server                    *Server
	current, pending          *wayland.Buffer
	attached                  bool
	lastW, lastH              int // last logged buffer size
	callbacks                 []*wayland.Callback
	role                      func(bool)
	destroyed                 bool
	// hotX, hotY is the click point of a cursor surface (logical).
	hotX, hotY int
	// on is the output the surface entered; scale is the last scale sent.
	on    *output
	scale float64
	// content is the last content built from this surface's own buffer;
	// has is false while no buffer is attached.
	content  ports.SurfaceContent
	has      bool
	identity uint64
	version  uint64
	// cachedTree is never modified once returned to emitContent.
	cachedTree                 []ports.Subsurface
	treeDirty                  bool
	commitFresh, commitSkipped bool
	queuedScale                int
	queuedBuffer               uint32
	sub                        subState
	// tearing is the surface's wp_tearing_control_v1; async is the
	// committed hint, pendingAsync the requested one.
	tearing                               *tearingHandler
	async, pendingAsync                   bool
	colorControl                          *colorSurface
	color, pendingColor                   SurfaceColor
	representation, pendingRepresentation surfaceRepresentation
	representationControl                 *representationSurface
	// Presentation constraints (fifo.go): pending fifo requests and commit
	// timestamp, the barrier, and the commits waiting to apply.
	fifo                        *fifoHandler
	timer                       *timerHandler
	pendingBarrier, pendingWait bool
	pendingTime                 time.Time
	barrier                     bool
	barrierAt                   time.Time
	queue                       []*update
	// contentType is the wp_content_type_v1; contentKind the committed type.
	contentType              *contentTypeHandler
	contentKind, pendingKind uint32
	// Damage requested since the last commit: in surface (logical) and
	// buffer pixels; committed is what the last commit changed, in buffer
	// pixels (full: everything), read by the window's damage history.
	pendingDamage, pendingBufDamage []ports.Rect
	committed                       damage
	// pendingFeedback are wp_presentation feedbacks for the next commit.
	pendingFeedback []*presentationtime.WpPresentationFeedback
	// Explicit sync (syncobj.go): the surface's syncobj object; the
	// committed points of the pending commit (pendingSync, with the wait
	// for its acquire point); and the current buffer's hold.
	sync        *syncState
	pendingSync *commitSync
	hold        syncHold
}

// commitSync is a commit's explicit-sync points and acquire wait.
type commitSync struct {
	acquire, release syncPoint
	wait             *syncWait
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
	return c
}

// tree is the root's content with its subsurfaces flattened.
func (s *surface) tree(id ports.WindowID) ports.SurfaceContent {
	c := s.contentWithColor()
	c.ID = id
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
	c.Async = s.async
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
	s.sendScale()
}

func (s *surface) Destroy(*wayland.Surface) {
	s.destroyed = true
	for _, fb := range s.pendingFeedback {
		discard(fb)
	}
	s.pendingFeedback = nil
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
	for _, cb := range s.callbacks {
		cb.Destroy()
	}
	s.callbacks = nil
	// The client may reuse the buffer on another surface.
	s.dropSync(s.pendingSync)
	s.pendingSync = nil
	if s.current != nil {
		s.server.releaseBuffer(s, s.current, s.hold)
		s.hold = syncHold{}
	}
	s.current, s.pending = nil, nil
	if s.role != nil {
		s.role(false)
	}
	if c := s.server.constraints[s]; c != nil {
		s.server.dropConstraint(c)
	}
}
func (s *surface) Attach(_ *wayland.Surface, b *wayland.Buffer, _, _ int32) {
	s.pending = b
	s.attached = true
}
func (s *surface) Frame(r *wayland.Surface, id uint32) {
	cb, err := wayland.NewCallback(r.Client(), 1, id, struct{}{})
	if err == nil {
		s.callbacks = append(s.callbacks, cb)
	}
}
func (s *surface) Commit(*wayland.Surface) {
	if s.layer != nil && s.attached && s.pending != nil && !s.layer.acked {
		s.layer.resource.PostError(uint32(wlrlayershell.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState), "buffer before configure ack")
		return
	}
	if s.xdg != nil && s.attached && s.pending != nil && !s.xdg.acked {
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

// applyCommit makes the pending state current.
func (s *surface) applyCommit() {
	oldW, oldH, oldSource := s.content.LogicalW, s.content.LogicalH, s.content.Source
	oldColor, oldRepresentation := s.color, s.representation
	fresh := s.attached && s.pending != nil
	s.commitFresh = fresh
	if !s.commitSkipped {
		s.queuedScale = s.pendingScale
		s.queuedBuffer = 0
		if s.pending != nil {
			s.queuedBuffer = s.pending.ID()
		}
	}
	if s.pendingBarrier {
		s.pendingBarrier = false
		s.setBarrier(time.Now())
	}
	s.pendingWait, s.pendingTime = false, time.Time{}
	if s.contentKind != s.pendingKind {
		s.contentKind = s.pendingKind
		s.server.log.Info().Uint64("id", uint64(s.root().windowID())).Uint32("content_type", s.contentKind).Msg("content type")
	}
	if s.pendingScale > 0 {
		s.bufferScale = s.pendingScale
	}
	hinted := s.async != s.pendingAsync || s.color != s.pendingColor || s.representation != s.pendingRepresentation
	s.async = s.pendingAsync
	s.color = s.pendingColor
	s.representation = s.pendingRepresentation
	if s.viewport != nil {
		s.viewport.commit()
		v := *s.viewport
		s.committedViewport = &v
	} else {
		s.committedViewport = nil
	}
	cs := s.pendingSync
	s.pendingSync = nil
	if s.attached {
		if s.current != nil && (s.pending == nil || s.current.Resource != s.pending.Resource) {
			s.server.releaseBuffer(s, s.current, s.hold)
		} else if cs != nil || s.pending == nil {
			// Same buffer again, or detached: the old points are done
			// with once the new ones take over.
			s.server.releaseBufferSync(s, s.hold)
		}
		s.hold = syncHold{}
		s.current = s.pending
		s.pending = nil
		s.attached = false
	}
	s.applySync(cs)
	if len(s.callbacks) > 0 {
		s.server.queueFrames(s.server.frameOutput(s), s.callbacks)
	}
	s.callbacks = nil
	if s.role != nil {
		s.role(s.current != nil)
	}
	if fresh {
		if state, ok := s.server.buffers[s.current.Resource]; ok {
			if c, ok := state.content(0); ok {
				if !s.validateViewport(c.Width, c.Height) {
					return
				}
				c.Source, _ = s.source(c.Width, c.Height)
				c.LogicalW, c.LogicalH = s.logicalSize(c.Width, c.Height)
				resized := !s.has || c.Width != s.content.Width || c.Height != s.content.Height || c.LogicalW != s.content.LogicalW || c.LogicalH != s.content.LogicalH
				s.commitDamage(true, resized, c.Width, c.Height)
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
	damaged := !fresh && s.current != nil && (len(s.pendingDamage) > 0 || len(s.pendingBufDamage) > 0)
	if !fresh {
		s.commitDamage(false, false, 0, 0)
		// A NULL attach is exempt from out_of_buffer: its content is
		// cleared below, so the retained buffer is not validated.
		if s.has && s.current != nil {
			if !s.validateViewport(s.content.Width, s.content.Height) {
				return
			}
			s.content.Source, _ = s.source(s.content.Width, s.content.Height)
			s.content.LogicalW, s.content.LogicalH = s.logicalSize(s.content.Width, s.content.Height)
		}
	}
	if s.current == nil {
		s.content, s.has = ports.SurfaceContent{}, false
	}
	if fresh || damaged || oldW != s.content.LogicalW || oldH != s.content.LogicalH || oldSource != s.content.Source || oldColor != s.color || oldRepresentation != s.representation {
		s.version++
		s.root().treeDirty = true
	}
	// The layout belongs to this update, not to the latest requests.
	moved := !sameLayout(s.sub.layout, s.sub.pendingLayout)
	if moved {
		s.root().treeDirty = true
	}
	// Queued updates can still reference the previous layout. Never mutate it.
	if moved {
		s.sub.layout = s.sub.pendingLayout
	}
	geometry := false
	if s.xdg != nil && s.xdg.pendingGeometry != s.xdg.geometry {
		s.xdg.geometry, geometry = s.xdg.pendingGeometry, true
	}
	s.commitConstraint(geometry)
	if s.xdg != nil && s.xdg.window != nil {
		s.xdg.window.afterCommit()
	}
	drawn := fresh || damaged || moved || geometry || hinted || s.sub.parent != nil || (s.has && (s.content.LogicalW != oldW || s.content.LogicalH != oldH || s.content.Source != oldSource))
	if drawn {
		if moved || geometry || oldW != s.content.LogicalW || oldH != s.content.LogicalH || oldSource != s.content.Source {
			s.committed = damage{full: true}
		}
		if s.server.applyingGraph {
			s.server.graphDrawn = true
		} else {
			s.redraw()
		}
	}
	fb := s.pendingFeedback
	s.pendingFeedback = nil
	if s.server.applyingGraph {
		s.server.graphFeedback = append(s.server.graphFeedback, graphFeedback{s, fb, fresh})
	} else {
		s.commitFeedback(fb, fresh)
	}
}
func (s *surface) Damage(_ *wayland.Surface, x, y, w, h int32) {
	if w > 0 && h > 0 && len(s.pendingDamage) <= maxDamageRects {
		s.pendingDamage = append(s.pendingDamage, ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)})
	}
}
func (s *surface) DamageBuffer(_ *wayland.Surface, x, y, w, h int32) {
	if w > 0 && h > 0 && len(s.pendingBufDamage) <= maxDamageRects {
		s.pendingBufDamage = append(s.pendingBufDamage, ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)})
	}
}

// commitDamage turns the requested damage into buffer pixels of a bw×bh
// buffer. Surface damage scales by the buffer/logical ratio, rounded out.
// A new size, too many rects or none at all with a new buffer (clients
// that do not report damage) is a full change.
func (s *surface) commitDamage(fresh, resized bool, bw, bh int) {
	surf, buf := s.pendingDamage, s.pendingBufDamage
	s.pendingDamage, s.pendingBufDamage = nil, nil
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
	for _, r := range surf {
		if lw <= 0 || lh <= 0 {
			s.committed = damage{full: true}
			return
		}
		// Clients may damage INT32_MAX-sized rects: clamp before scaling.
		x0, y0 := max(r.X, 0), max(r.Y, 0)
		x1, y1 := min(r.X+min(r.W, lw), lw), min(r.Y+min(r.H, lh), lh)
		if src, ok := s.source(bw, bh); ok {
			add(image.Rect(int(src[0]+float32(x0)*src[2]/float32(lw)), int(src[1]+float32(y0)*src[3]/float32(lh)),
				int(math.Ceil(float64(src[0]+float32(x1)*src[2]/float32(lw)))), int(math.Ceil(float64(src[1]+float32(y1)*src[3]/float32(lh))))))
		} else {
			add(image.Rect(x0*bw/lw, y0*bh/lh, (x1*bw+lw-1)/lw, (y1*bh+lh-1)/lh))
		}
	}
	s.committed = d
}
func (*surface) SetOpaqueRegion(*wayland.Surface, *wayland.Region) {}
func (*surface) SetInputRegion(*wayland.Surface, *wayland.Region)  {}
func (*surface) SetBufferTransform(*wayland.Surface, int32)        {}
func (s *surface) SetBufferScale(r *wayland.Surface, v int32) {
	if v < 1 {
		r.PostError(uint32(wayland.SurfaceErrorInvalidScale), "buffer scale must be positive")
		return
	}
	s.pendingScale = int(v)
}
func (*surface) Offset(*wayland.Surface, int32, int32) {}
func (*surface) GetRelease(*wayland.Surface, uint32)   {}
