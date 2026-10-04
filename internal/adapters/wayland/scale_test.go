package wayland

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/fractionalscale"
	"github.com/bnema/go-wayland-bindings/server/viewporter"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

type scaleProxy struct {
	wlturbo.BaseProxy
	values chan uint32
}

func (p *scaleProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 0 {
		p.values <- e.Uint32()
	}
}

// surfaceScaleProxy records wl_surface.preferred_buffer_scale (opcode 2).
type surfaceScaleProxy struct {
	wlturbo.BaseProxy
	scales chan int32
}

func (p *surfaceScaleProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 2 {
		p.scales <- e.Int32()
	}
}

// surfaceTransformProxy records wl_surface.preferred_buffer_transform (opcode 3).
type surfaceTransformProxy struct {
	wlturbo.BaseProxy
	transforms chan uint32
}

func (p *surfaceTransformProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 3 {
		p.transforms <- e.Uint32()
	}
}

func contentServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, chan ports.SurfaceContent, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	contents := make(chan ports.SurfaceContent, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events, Commands: commands, Contents: contents}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, events, commands, contents, dir
}

// A v6 surface learns the transform of its output when it differs from
// normal (the protocol default), and again only when it changes. A cursor
// surface never does: cursor buffers must stay untransformed.
func TestPreferredBufferTransform(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	info := testOutputs[0].Info
	setOutputs := func(tr ports.BufferTransform, scale float64) {
		if !s.display.Do(func() {
			s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: info, Width: info.Height, Height: info.Width, Scale: scale, Transform: tr}}})
		}) {
			t.Fatal("display stopped")
		}
	}
	newSurface := func(c *wlturbo.Display, comp uint32) (uint32, func() []uint32) {
		surf := c.AllocateID()
		p := &surfaceTransformProxy{transforms: make(chan uint32, 8)}
		p.SetID(surf)
		registerWireProxy(c, p)
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
		return surf, func() []uint32 {
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			var got []uint32
			for len(p.transforms) > 0 {
				got = append(got, <-p.transforms)
			}
			return got
		}
	}
	setOutputs(0, 1)
	c := protocolClient(t, s, dir)
	g, _ := c.Registry().FindGlobal("wl_compositor")
	comp, err := bindWireID(c, g.Name, g.Interface, 6)
	if err != nil {
		t.Fatal(err)
	}
	seat := bindProtocol(t, c, "wl_seat")
	registerProtocol(t, c, seat)
	pointer := c.AllocateID()
	registerProtocol(t, c, pointer)
	requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)

	_, normal := newSurface(c, comp)
	cursor, cursorSeen := newSurface(c, comp)
	if got := normal(); len(got) != 0 {
		t.Fatalf("normal output sent transforms %v", got)
	}
	cursorSeen()
	requestProtocol(t, c, pointer, wayland.PointerRequestSetCursor, uint32(1), cursor, int32(0), int32(0))

	setOutputs(1, 1)
	if got := normal(); !slices.Equal(got, []uint32{1}) {
		t.Fatalf("rotated transforms %v, want [1]", got)
	}
	if got := cursorSeen(); len(got) != 0 {
		t.Fatalf("cursor surface got transforms %v", got)
	}
	// A scale change alone does not repeat the transform.
	setOutputs(1, 2)
	if got := normal(); len(got) != 0 {
		t.Fatalf("unchanged transform resent: %v", got)
	}
	setOutputs(3, 2)
	if got := normal(); !slices.Equal(got, []uint32{3}) {
		t.Fatalf("changed transforms %v, want [3]", got)
	}
	setOutputs(0, 2)
	if got := normal(); !slices.Equal(got, []uint32{0}) {
		t.Fatalf("back to normal: %v, want [0]", got)
	}
	if got := cursorSeen(); len(got) != 0 {
		t.Fatalf("cursor surface got transforms %v", got)
	}
}

func TestFractionalScaleAndViewport(t *testing.T) {
	s, events, commands, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	// wl_surface.preferred_buffer_scale needs wl_compositor v6.
	g, _ := c.Registry().FindGlobal("wl_compositor")
	comp, err := bindWireID(c, g.Name, g.Interface, 6)
	if err != nil {
		t.Fatal(err)
	}
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	fm := bindProtocol(t, c, "wp_fractional_scale_manager_v1")
	vpm := bindProtocol(t, c, "wp_viewporter")
	registerProtocol(t, c, shm) // wl_shm sends its formats right away
	// New IDs must be used in allocation order.
	surf := c.AllocateID()
	bufferScales := make(chan int32, 4)
	sp := &surfaceScaleProxy{scales: bufferScales}
	sp.SetID(surf)
	registerWireProxy(c, sp)
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	frac := c.AllocateID()
	values := make(chan uint32, 4)
	proxy := &scaleProxy{values: values}
	proxy.SetID(frac)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, fm, fractionalscale.WpFractionalScaleManagerV1RequestGetFractionalScale, frac, surf)
	vp := c.AllocateID()
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	registerProtocol(t, c, vp)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if v := <-values; v != 120 {
		t.Fatalf("initial scale %d", v)
	}
	// Core switches to 1.5: the surface gets 180/120.
	commands <- ports.SetOutputs{Outputs: ports.Layout{{Info: testOutputs[0].Info, Width: 1280, Height: 720, Scale: 1.5}}}
	// Events are read by Roundtrip; the command is applied asynchronously.
	var got uint32
	for deadline := time.Now().Add(2 * time.Second); got == 0 && time.Now().Before(deadline); {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case got = <-values:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got != 180 {
		t.Fatalf("scale %d", got)
	}
	// Clients without fractional scale get the integer scale rounded up.
	var last int32
	for len(bufferScales) > 0 {
		last = <-bufferScales
	}
	if last != 2 {
		t.Fatalf("preferred_buffer_scale %d", last)
	}
	// A 30x15 buffer shown at 20x10 logical through the viewport.
	xdg := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 4)
	cp := &configureProxy{serial: serials}
	cp.SetID(xdg)
	registerWireProxy(c, cp)
	top := c.AllocateID()
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, <-serials)
	fd, err := unix.MemfdCreate("scaled", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	size := int32(30 * 15 * 4)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, size); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(30), int32(15), int32(120), uint32(0))
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(20), int32(10))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	mapped(t, events, 2*time.Second)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-contents:
			if got.SHM == nil {
				continue
			}
			if got.Width != 30 || got.Height != 15 || got.LogicalW != 20 || got.LogicalH != 10 {
				t.Fatalf("%+v", got)
			}
			goto viewportOnly
		case <-deadline:
			t.Fatal("no content")
		}
	}
viewportOnly:
	// Changing only the destination must update content with the current buffer.
	for _, size := range [][2]int32{{1, 1}, {7, 3}} {
		requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, size[0], size[1])
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-contents:
			if got.LogicalW != int(size[0]) || got.LogicalH != int(size[1]) || got.Width != 30 || got.Height != 15 {
				t.Fatalf("viewport-only content: %+v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no viewport-only content")
		}
	}
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(256), int32(256), int32(5*256), int32(4*256))
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(-1), int32(-1))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if got.LogicalW != 5 || got.LogicalH != 4 || got.Source != [4]float32{1, 1, 5, 4} {
			t.Fatalf("source-only crop: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no crop content")
	}
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(2), int32(3))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-contents:
		if got.LogicalW != 2 || got.LogicalH != 3 || got.Source != [4]float32{1, 1, 5, 4} {
			t.Fatalf("source and destination: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no source and destination content")
	}
}

func TestBufferScaleLogicalSize(t *testing.T) {
	st := &surface{bufferScale: 2}
	if w, h := st.logicalSize(200, 100); w != 100 || h != 50 {
		t.Fatal(w, h)
	}
	st.committedViewport = viewportState{destW: 7, destH: 3, dest: true}
	if w, h := st.logicalSize(200, 100); w != 7 || h != 3 {
		t.Fatal(w, h)
	}
}

func TestViewportBadSource(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	surf, vp := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	registerProtocol(t, c, vp)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(-256), int32(0), int32(256), int32(256))
	expectProtocolError(t, c, vp, uint32(viewporter.WpViewportErrorBadValue))
}

func TestViewportApplyErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		width       int32
		destination bool
		code        viewporter.WpViewportError
	}{
		{"bad_size", 128, false, viewporter.WpViewportErrorBadSize},
		{"out_of_buffer", 512, true, viewporter.WpViewportErrorOutOfBuffer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, dir := contentServer(t)
			c := protocolClient(t, s, dir)
			comp := bindProtocol(t, c, "wl_compositor")
			vpm := bindProtocol(t, c, "wp_viewporter")
			shm := bindProtocol(t, c, "wl_shm")
			registerProtocol(t, c, shm)
			surf, vp := c.AllocateID(), c.AllocateID()
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
			requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
			registerProtocol(t, c, vp)
			fd, err := unix.MemfdCreate("viewport", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if err := unix.Ftruncate(fd, 4); err != nil {
				t.Fatal(err)
			}
			pool, buf := c.AllocateID(), c.AllocateID()
			registerProtocol(t, c, pool)
			registerProtocol(t, c, buf)
			if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
				t.Fatal(err)
			}
			requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1), int32(1), int32(4), uint32(0))
			requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), tc.width, int32(256))
			if tc.destination {
				requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(1), int32(1))
			}
			requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
			requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
			expectProtocolError(t, c, vp, uint32(tc.code))
		})
	}
}

// A client rounds the source position and size to 1/256 independently, so
// their sum can pass the buffer edge by a unit: that is accepted. The
// values are a real rejected source (y 213.86, height 1946.14 on 2160 rows).
// One pixel past the edge is still out_of_buffer.
func TestViewportSourceEdgeRounding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		y, h  int32
		error bool
	}{
		{"rounded edge", 54749, 498212, false},
		{"one pixel past", 54749, 498212 + 256, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, events, _, contents, dir := contentServer(t)
			c := protocolClient(t, s, dir)
			win, surf, _ := surfaceMapper(t, c, events)()
			drainContents(contents)
			vpm := bindProtocol(t, c, "wp_viewporter")
			shm := bindProtocol(t, c, "wl_shm")
			registerProtocol(t, c, shm)
			vp := c.AllocateID()
			requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
			registerProtocol(t, c, vp)
			const w, h = 4, 2160
			fd, err := unix.MemfdCreate("viewport-edge", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if err := unix.Ftruncate(fd, w*h*4); err != nil {
				t.Fatal(err)
			}
			pool, buf := c.AllocateID(), c.AllocateID()
			registerProtocol(t, c, pool)
			registerProtocol(t, c, buf)
			if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(w*h*4)); err != nil {
				t.Fatal(err)
			}
			requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(w), int32(h), int32(w*4), uint32(0))
			requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(w), int32(100))
			requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), tc.y, int32(w*256), tc.h)
			requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
			requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
			if tc.error {
				expectProtocolError(t, c, vp, uint32(viewporter.WpViewportErrorOutOfBuffer))
				return
			}
			roundtrip(t, c)
			select {
			case got := <-contents:
				if got.ID != win.ID || got.Source[1]+got.Source[3] > h || got.Source[1] == 0 {
					t.Fatalf("published source outside buffer: %+v", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing viewport publication")
			}
		})
	}
}

// A NULL attach unmaps the surface: viewporter.xml exempts it from
// out_of_buffer, so a source larger than the previous buffer is not an error.
// A later commit that keeps the retained buffer is still validated.
func TestViewportNullAttachSkipsBufferBounds(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	surf, vp := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	registerProtocol(t, c, vp)
	buf := shmBuffer(t, c)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(256), int32(256))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	roundtrip(t, c)

	// Oversized source with a NULL attach: no error.
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetSource, int32(0), int32(0), int32(512), int32(256))
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	roundtrip(t, c)

	// Reattach the 1x1 buffer: the oversized source now applies to it.
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	expectProtocolError(t, c, vp, uint32(viewporter.WpViewportErrorOutOfBuffer))
}

func TestViewportBadDestination(t *testing.T) {
	s, _, _, _, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	vpm := bindProtocol(t, c, "wp_viewporter")
	surf, vp := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	requestProtocol(t, c, vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	registerProtocol(t, c, vp)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(0), int32(10))
	expectProtocolError(t, c, vp, uint32(viewporter.WpViewportErrorBadValue))
}
