package wayland

import (
	"context"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/cursorshape"
	"github.com/bnema/purego-libwayland/protocol/tabletv2"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// The client under the pointer picks the cursor: a theme shape
// (wp_cursor_shape_v1), its own surface (wl_pointer.set_cursor) or none.
// Changes go to the outputs latest first; a pointer focus change resets
// the cursor to the arrow until the new client sets one.

// maxCursorSide bounds client cursor images read from shm.
const maxCursorSide = 256

// cursorShapes are the CSS names of wp_cursor_shape_device_v1 shapes, by
// enum value.
var cursorShapes = [...]string{
	1: "default", "context-menu", "help", "pointer", "progress", "wait", "cell",
	"crosshair", "text", "vertical-text", "alias", "copy", "move", "no-drop",
	"not-allowed", "grab", "grabbing", "e-resize", "n-resize", "ne-resize",
	"nw-resize", "s-resize", "se-resize", "sw-resize", "w-resize", "ew-resize",
	"ns-resize", "nesw-resize", "nwse-resize", "col-resize", "row-resize",
	"all-scroll", "zoom-in", "zoom-out", "dnd-ask", "all-resize",
}

func registerCursorShape(d *server.Display, s *Server) error {
	return cursorshape.NewWpCursorShapeManagerV1Global(d, 2, func(c server.Client, v, id uint32) {
		_, _ = cursorshape.NewWpCursorShapeManagerV1(c, int32(v), id, cursorShapeManager{s})
	})
}

type cursorShapeManager struct{ server *Server }

func (cursorShapeManager) Destroy(r *cursorshape.WpCursorShapeManagerV1) { r.Destroy() }
func (m cursorShapeManager) GetPointer(r *cursorshape.WpCursorShapeManagerV1, id uint32, _ *wayland.Pointer) {
	_, _ = cursorshape.NewWpCursorShapeDeviceV1(r.Client(), r.Version(), id, cursorShapeDevice(m))
}

// GetTabletToolV2 is unreachable: nefertty has no tablet global.
func (cursorShapeManager) GetTabletToolV2(*cursorshape.WpCursorShapeManagerV1, uint32, *tabletv2.ZwpTabletToolV2) {
}

type cursorShapeDevice struct{ server *Server }

func (cursorShapeDevice) Destroy(r *cursorshape.WpCursorShapeDeviceV1) { r.Destroy() }
func (d cursorShapeDevice) SetShape(r *cursorshape.WpCursorShapeDeviceV1, _ uint32, shape uint32) {
	last := cursorshape.WpCursorShapeDeviceV1ShapeZoomOut
	if r.Version() >= 2 {
		last = cursorshape.WpCursorShapeDeviceV1ShapeAllResize
	}
	if shape < uint32(cursorshape.WpCursorShapeDeviceV1ShapeDefault) || shape > uint32(last) {
		r.PostError(uint32(cursorshape.WpCursorShapeDeviceV1ErrorInvalidShape), "invalid shape")
		return
	}
	s := d.server
	if !s.hasPointerFocus(r.Client()) {
		return
	}
	s.cursorSurface = nil
	s.setCursor(ports.CursorChange{Shape: cursorShapes[shape]})
}

type pointer struct{ server *Server }

func (p pointer) SetCursor(r *wayland.Pointer, _ uint32, surf *wayland.Surface, hotX, hotY int32) {
	s := p.server
	var state *surface
	if surf != nil {
		state = s.surfaces[surf.Resource]
		if state == nil {
			return
		}
		if state.kind != roleNone && state.kind != roleCursor {
			r.PostError(uint32(wayland.PointerErrorRole), "surface already has another role")
			return
		}
		state.kind = roleCursor
		state.role = func(bool) {
			if s.cursorSurface == state {
				s.setCursor(state.cursorImage())
			}
		}
	}
	if !s.hasPointerFocus(r.Client()) {
		return
	}
	if state == nil {
		s.cursorSurface = nil
		s.setCursor(ports.CursorChange{Hidden: true})
		return
	}
	s.cursorSurface, state.hotX, state.hotY = state, int(hotX), int(hotY)
	s.setCursor(state.cursorImage())
}
func (pointer) Release(r *wayland.Pointer) { r.Destroy() }

// hasPointerFocus reports whether the client owns the window under the pointer.
func (s *Server) hasPointerFocus(c server.Client) bool {
	w := s.windows[s.pointerFocus]
	return w != nil && w.mapped && w.xdg.resource.Client() == c
}

// cursorImage copies the committed shm buffer of a cursor surface. No
// buffer hides the cursor; an unreadable one falls back to the arrow.
func (s *surface) cursorImage() ports.CursorChange {
	if s.current == nil {
		return ports.CursorChange{Hidden: true}
	}
	b, ok := s.server.buffers[s.current.Resource].(*buffer)
	if !ok || b.width > maxCursorSide || b.height > maxCursorSide {
		return ports.CursorChange{}
	}
	img := ports.CursorImage{W: b.width, H: b.height, Pixels: make([]byte, b.width*b.height*4)}
	for y := range b.height {
		row := img.Pixels[y*b.width*4 : (y+1)*b.width*4]
		if _, err := b.pool.file.ReadAt(row, int64(b.offset+y*b.stride)); err != nil {
			return ports.CursorChange{}
		}
		if b.format == uint32(wayland.ShmFormatXrgb8888) {
			for i := 3; i < len(row); i += 4 {
				row[i] = 255
			}
		}
	}
	scale := max(s.bufferScale, 1)
	img.HotX, img.HotY = min(max(s.hotX*scale, 0), b.width-1), min(max(s.hotY*scale, 0), b.height-1)
	return ports.CursorChange{Image: &img, Scale: scale}
}

// setCursor queues a cursor change; only the latest is delivered.
func (s *Server) setCursor(c ports.CursorChange) {
	if s.channels.Cursors == nil {
		return
	}
	s.log.Debug().Str("shape", c.Shape).Bool("client", c.Image != nil).Bool("hidden", c.Hidden).Msg("cursor")
	s.cursorMu.Lock()
	s.cursorLatest, s.cursorQueued = c, true
	s.cursorMu.Unlock()
	select {
	case s.cursorReady <- struct{}{}:
	default:
	}
}

func (s *Server) forwardCursors(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cursorReady:
		}
		s.cursorMu.Lock()
		c, ok := s.cursorLatest, s.cursorQueued
		s.cursorQueued = false
		s.cursorMu.Unlock()
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case s.channels.Cursors <- c:
		}
	}
}
