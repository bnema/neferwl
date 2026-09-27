package wayland

import (
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"image"
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
	wl       *wayland.Surface
	viewport *viewport
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
	content ports.SurfaceContent
	has     bool
	sub     subState
	// tearing is the surface's wp_tearing_control_v1; async is the
	// committed hint, pendingAsync the requested one.
	tearing             *tearingHandler
	async, pendingAsync bool
	colorControl        *colorSurface
	color, pendingColor SurfaceColor
	// Presentation constraints (fifo.go): pending fifo requests and commit
	// timestamp, the barrier, and the commits waiting to apply.
	fifo                        *fifoHandler
	timer                       *timerHandler
	pendingBarrier, pendingWait bool
	pendingTime                 time.Time
	barrier                     bool
	barrierAt                   time.Time
	queue                       []update
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
	role         *wayland.Subsurface
	parent       *surface
	children     []*surface
	x, y         int // committed position, from the parent origin
	pendX, pendY int
	moved        bool // a position waits for the parent commit
	below        bool
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

// tree is the root's content with its subsurfaces flattened.
func (s *surface) tree(id ports.WindowID) ports.SurfaceContent {
	c := s.content
	c.ID = id
	c.Children = nil
	for _, ch := range s.sub.children {
		ch.appendTree(&c.Children, ch.sub.x, ch.sub.y, ch.sub.below)
	}
	if s.xdg != nil {
		c.Geometry = s.xdg.geometry
	}
	c.Async = s.async
	return c
}

// appendTree flattens a subsurface at (x, y) from the root: its children
// below it, itself, then its children above.
func (s *surface) appendTree(out *[]ports.Subsurface, x, y int, below bool) {
	for _, ch := range s.sub.children {
		if ch.sub.below {
			ch.appendTree(out, x+ch.sub.x, y+ch.sub.y, below)
		}
	}
	if s.has {
		*out = append(*out, ports.Subsurface{X: x, Y: y, Below: below, SurfaceContent: s.content})
	}
	for _, ch := range s.sub.children {
		if !ch.sub.below {
			ch.appendTree(out, x+ch.sub.x, y+ch.sub.y, below)
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
	s.sub.parent = nil
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
	s.dropQueue()
	if s.server.cursorSurface == s {
		// The pointer keeps no cursor until the client sets another.
		s.server.cursorSurface = nil
		s.server.setCursor(ports.CursorChange{Hidden: true})
	}
	s.detach()
	for _, ch := range s.sub.children {
		ch.sub.parent = nil
	}
	s.sub.children = nil
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
	if !s.checkSyncCommit() {
		return
	}
	s.takeSyncPoints()
	// A commit behind a fifo barrier, a future timestamp or an acquire
	// point without a fence yet waits (fifo.go).
	if s.mustWait(time.Now()) {
		s.queueUpdate()
		return
	}
	s.applyCommit()
}

// applyCommit makes the pending state current.
func (s *surface) applyCommit() {
	fresh := s.attached && s.pending != nil
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
	hinted := s.async != s.pendingAsync
	s.async = s.pendingAsync
	s.color = s.pendingColor
	if s.viewport != nil {
		s.viewport.commit()
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
	if !fresh {
		s.commitDamage(false, false, 0, 0)
	}
	if s.current == nil {
		s.content, s.has = ports.SurfaceContent{}, false
	}
	// Subsurface positions apply on the parent commit.
	moved := false
	for _, ch := range s.sub.children {
		if ch.sub.moved {
			ch.sub.x, ch.sub.y, ch.sub.moved = ch.sub.pendX, ch.sub.pendY, false
			moved = true
		}
	}
	geometry := false
	if s.xdg != nil && s.xdg.pendingGeometry != s.xdg.geometry {
		s.xdg.geometry, geometry = s.xdg.pendingGeometry, true
	}
	s.commitConstraint(geometry)
	if s.xdg != nil && s.xdg.window != nil {
		s.xdg.window.afterCommit()
	}
	drawn := fresh || moved || geometry || hinted || s.sub.parent != nil
	if drawn {
		if moved || geometry {
			s.committed = damage{full: true}
		}
		s.redraw()
	}
	fb := s.pendingFeedback
	s.pendingFeedback = nil
	s.commitFeedback(fb, fresh)
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
		add(image.Rect(x0*bw/lw, y0*bh/lh, (x1*bw+lw-1)/lw, (y1*bh+lh-1)/lh))
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
