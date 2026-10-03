package wayland

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/linuxdmabuf"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

var linearARGB = ports.DMABufFormat{Format: 'A' | 'R'<<8 | '2'<<16 | '4'<<24}

func dmabufServer(t *testing.T, sup ports.DMABufSupport) (*Server, chan ports.ClientEvent, chan ports.SurfaceContent, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	contents := make(chan ports.SurfaceContent, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, DMABuf: sup}, Channels{Events: events, Contents: contents}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run hung")
		}
	})
	return s, events, contents, dir
}

// Without renderer formats there is no linux-dmabuf global.
func TestNoDMABufWithoutFormats(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{})
	c := protocolClient(t, s, dir)
	if _, ok := c.Registry().FindGlobal("zwp_linux_dmabuf_v1"); ok {
		t.Fatal("zwp_linux_dmabuf_v1 advertised")
	}
}

type paramsProxy struct {
	wlturbo.BaseProxy
	result chan uint16
}

func (p *paramsProxy) Dispatch(e *wlturbo.Event) { p.result <- e.Opcode }

// An unadvertised format is a protocol error from v4 and `failed` before; a
// plane past the end of its file is a protocol error.
func TestDMABufParams(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	c := protocolClient(t, s, dir)
	g, ok := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	if !ok || g.Version != dmabufVersion {
		t.Fatalf("global %+v", g)
	}
	dm, err := bindWireID(c, g.Name, g.Interface, dmabufVersion)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.MemfdCreate("fake-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 256*16); err != nil {
		t.Fatal(err)
	}
	create := func(format uint32, height int32) (uint32, chan uint16) {
		p := c.AllocateID()
		requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
		proxy := &paramsProxy{result: make(chan uint16, 2)}
		proxy.SetID(p)
		registerWireProxy(c, proxy)
		if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
			t.Fatal(err)
		}
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), height, format, uint32(0))
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		return p, proxy.result
	}
	// Supported: created (import errors surface in the renderer).
	_, result := create(linearARGB.Format, 16)
	if op := <-result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventCreated) {
		t.Fatalf("event %d", op)
	}
	// Taller than the file: out of bounds.
	big := c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, big)
	if err := wireRequest(c, big, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, big, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(17), linearARGB.Format, uint32(0))
	expectProtocolError(t, c, big, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorOutOfBounds))
	// Each case below needs its own connection: a protocol error kills it.
	t.Run("unadvertised format v6", func(t *testing.T) {
		c, p, _ := createNV12(t, dmabufVersion, false, 0)
		expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidFormat))
	})
	t.Run("unadvertised format v6 immed", func(t *testing.T) {
		c, p, _ := createNV12(t, dmabufVersion, true, 0)
		expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidFormat))
	})
	// Flags are checked first: a y-inverted buffer fails, whatever its format.
	t.Run("flags before format v6", func(t *testing.T) {
		c, _, proxy := createNV12(t, dmabufVersion, false, 1)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if op := <-proxy.result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventFailed) {
			t.Fatalf("event %d", op)
		}
	})
	// Before v4 there is no feedback to follow: the client gets `failed`,
	// or invalid_wl_buffer for an immediate create.
	t.Run("unadvertised format v3 immed", func(t *testing.T) {
		c, p, _ := createNV12(t, 3, true, 0)
		expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidWlBuffer))
	})
	t.Run("unadvertised format v3", func(t *testing.T) {
		c, _, proxy := createNV12(t, 3, false, 0)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if op := <-proxy.result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventFailed) {
			t.Fatalf("event %d", op)
		}
	})
}

// createNV12 sends a one-plane create (or create_immed) of NV12, which the
// server does not list, on a fresh connection bound at the given version.
func createNV12(t *testing.T, version uint32, immed bool, flags uint32) (*wlturbo.Display, uint32, *paramsProxy) {
	t.Helper()
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	c := protocolClient(t, s, dir)
	dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", version)
	fd, err := unix.MemfdCreate("fake-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.Ftruncate(fd, 256*16); err != nil {
		t.Fatal(err)
	}
	p := c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
	proxy := &paramsProxy{result: make(chan uint16, 2)}
	proxy.SetID(p)
	registerWireProxy(c, proxy)
	if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	if immed {
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreateImmed, c.AllocateID(), int32(64), int32(16), uint32(fourccNV12), flags)
	} else {
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), uint32(fourccNV12), flags)
	}
	return c, p, proxy
}

// set_sampling_device takes an 8-byte dev_t, before the params are used.
func TestDMABufSamplingDevice(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	fd, err := unix.MemfdCreate("fake-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 256*16); err != nil {
		t.Fatal(err)
	}
	newParams := func(c *wlturbo.Display, dm uint32) (uint32, *paramsProxy) {
		p := c.AllocateID()
		requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
		proxy := &paramsProxy{result: make(chan uint16, 2)}
		proxy.SetID(p)
		registerWireProxy(c, proxy)
		return p, proxy
	}
	setDevice := func(c *wlturbo.Display, p uint32, dev []byte) {
		t.Helper()
		if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestSetSamplingDevice), nil, dev); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("accepted", func(t *testing.T) {
		c := protocolClient(t, s, dir)
		dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", dmabufVersion)
		p, proxy := newParams(c, dm)
		setDevice(c, p, devBytes(42))
		if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
			t.Fatal(err)
		}
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), linearARGB.Format, uint32(0))
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if op := <-proxy.result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventCreated) {
			t.Fatalf("event %d", op)
		}
	})
	t.Run("bad size", func(t *testing.T) {
		c := protocolClient(t, s, dir)
		dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", dmabufVersion)
		p, _ := newParams(c, dm)
		setDevice(c, p, make([]byte, 4))
		expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidDevTSize))
	})
	t.Run("after create", func(t *testing.T) {
		c := protocolClient(t, s, dir)
		dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", dmabufVersion)
		p, _ := newParams(c, dm)
		if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
			t.Fatal(err)
		}
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), linearARGB.Format, uint32(0))
		setDevice(c, p, devBytes(42))
		expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorAlreadyUsed))
	})
}

// A reused params object is a protocol error.
func TestDMABufParamsReuse(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	c := protocolClient(t, s, dir)
	g, _ := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	dm, err := bindWireID(c, g.Name, g.Interface, dmabufVersion)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.MemfdCreate("fake-dmabuf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 256*16); err != nil {
		t.Fatal(err)
	}
	p := c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
	proxy := &paramsProxy{result: make(chan uint16, 2)}
	proxy.SetID(p)
	registerWireProxy(c, proxy)
	if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), linearARGB.Format, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), linearARGB.Format, uint32(0))
	expectProtocolError(t, c, p, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorAlreadyUsed))
}

// weston-simple-dmabuf-egl renders on the GPU and sends dmabufs: its
// contents reach the renderer as DMABuf, never as pixels.
func TestWestonDMABuf(t *testing.T) {
	tool, err := exec.LookPath("weston-simple-dmabuf-egl")
	if err != nil {
		t.Skip("weston-simple-dmabuf-egl not installed")
	}
	// Linear buffers: every GPU allocates them, and the client picks from
	// what it is offered.
	xrgb := ports.DMABufFormat{Format: fourccXRGB}
	s, events, contents, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB, xrgb}})
	_, _, output := lifecycleClient(t, tool, s, dir, nil)
	w := mapped(t, events, 5*time.Second)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-contents:
			if c.ID != w.ID || c.DMABuf == nil || c.SHM != nil || len(c.DMABuf.Planes) == 0 || c.DMABuf.Planes[0].File == nil {
				t.Fatalf("content %+v", c)
			}
			return
		case <-deadline:
			t.Fatalf("no dmabuf content: %s", output.String())
		}
	}
}

// feedbackProxy records each feedback round (up to done).
type feedbackProxy struct {
	wlturbo.BaseProxy
	rounds chan round
	cur    round
	open   tranche
}

// round is what one feedback round carried.
type round struct {
	tranches    []tranche
	tables      int
	mainDevices int
}

type tranche struct {
	flags   uint32
	indices []uint16
}

func (p *feedbackProxy) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventFormatTable:
		p.cur.tables++
		if fd := e.Fd(); fd != 0 {
			unix.Close(int(fd))
		}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventMainDevice:
		p.cur.mainDevices++
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheFlags:
		p.open.flags = e.Uint32()
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheFormats:
		a := e.Array()
		for i := 0; i+1 < len(a); i += 2 {
			p.open.indices = append(p.open.indices, uint16(a[i])|uint16(a[i+1])<<8)
		}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheDone:
		p.cur.tranches, p.open = append(p.cur.tranches, p.open), tranche{}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventDone:
		p.rounds <- p.cur
		p.cur = round{}
	}
}

// scanoutHarness is a server with commands and output formats channels, a
// mapped window, and a surface feedback bound at the given version.
type scanoutHarness struct {
	t        *testing.T
	c        *wlturbo.Display
	s        *Server
	w        ports.WindowMapped
	fb       uint32
	proxy    *feedbackProxy
	commands chan ports.ClientCommand
	formats  chan ports.OutputFormats
}

func newScanoutHarness(t *testing.T, sup ports.DMABufSupport, version uint32) *scanoutHarness {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	h := &scanoutHarness{t: t, commands: make(chan ports.ClientCommand, 16), formats: make(chan ports.OutputFormats, 1)}
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, DMABuf: sup}, Channels{Events: events, Commands: h.commands, OutputFormats: h.formats}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.c = protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, h.c, events)()
	h.w = w
	// Our configures must not block the mapper's one-slot proxy.
	registerProtocol(t, h.c, xdg)
	dm := bindVersion(t, h.c, "zwp_linux_dmabuf_v1", version)
	h.fb = h.c.AllocateID()
	h.proxy = &feedbackProxy{rounds: make(chan round, 8)}
	h.proxy.SetID(h.fb)
	registerWireProxy(h.c, h.proxy)
	requestProtocol(t, h.c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestGetSurfaceFeedback, h.fb, surf)
	return h
}

// next waits for the next feedback round.
func (h *scanoutHarness) next(what string) round {
	h.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if err := h.c.Roundtrip(); err != nil {
			h.t.Fatal(err)
		}
		select {
		case r := <-h.proxy.rounds:
			return r
		case <-deadline:
			h.t.Fatalf("no feedback: %s", what)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// A fullscreen surface on an output with scanout formats gets a scanout
// tranche first; leaving fullscreen sends the renderer tranche alone. From
// v6 the table comes once, main_device never, and the renderer tranche is
// flagged for sampling.
func TestDMABufScanoutTranche(t *testing.T) {
	tiled := ports.DMABufFormat{Format: linearARGB.Format, Modifier: 0x0200000000000001}
	sup := ports.DMABufSupport{Device: 1, Formats: []ports.DMABufFormat{linearARGB, tiled}}
	h := newScanoutHarness(t, sup, dmabufVersion)
	c, s, w, fb, commands, formats := h.c, h.s, h.w, h.fb, h.commands, h.formats
	scanout := uint32(linuxdmabuf.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout)
	sampling := uint32(linuxdmabuf.ZwpLinuxDmabufFeedbackV1TrancheFlagsSampling)
	if r := h.next("initial"); len(r.tranches) != 1 || r.tranches[0].flags != sampling || len(r.tranches[0].indices) != 2 || r.tables != 1 || r.mainDevices != 0 {
		t.Fatalf("tiled window: %+v", r)
	}
	formats <- ports.OutputFormats{Output: "HEADLESS-1", Device: 7, Formats: []ports.DMABufFormat{tiled}}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	r := h.next("fullscreen")
	for len(r.tranches) == 1 { // the formats may land after the configure
		if r.tables != 0 || r.mainDevices != 0 {
			t.Fatalf("resent table or main device: %+v", r)
		}
		r = h.next("fullscreen with formats")
	}
	if len(r.tranches) != 2 || r.tranches[0].flags != scanout || len(r.tranches[0].indices) != 1 || r.tranches[0].indices[0] != 1 || r.tranches[1].flags != sampling || r.tables != 0 || r.mainDevices != 0 {
		t.Fatalf("fullscreen: %+v", r)
	}
	// Hidden, the window loses its scanout tranche; shown, it gets it back.
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1"}
	if r := h.next("hidden"); len(r.tranches) != 1 || r.tables != 0 {
		t.Fatalf("hidden fullscreen: %+v", r)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	if r := h.next("shown again"); len(r.tranches) != 2 || r.tranches[0].flags != scanout || r.tables != 0 {
		t.Fatalf("shown fullscreen: %+v", r)
	}
	// Fullscreen on another output: its formats (none) apply at once.
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "OTHER-1", Visible: true}
	if r := h.next("other output"); len(r.tranches) != 1 || r.tables != 0 {
		t.Fatalf("moved output: %+v", r)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 800, Height: 600, Output: "HEADLESS-1", Visible: true}
	if r := h.next("tiled again"); len(r.tranches) != 1 || r.tranches[0].flags != sampling || r.tables != 0 {
		t.Fatalf("left fullscreen: %+v", r)
	}
	// A destroyed feedback gets nothing more (a protocol error would
	// kill the connection) and leaves the server's list.
	requestProtocol(t, c, fb, linuxdmabuf.ZwpLinuxDmabufFeedbackV1RequestDestroy)
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	formats <- ports.OutputFormats{Output: "HEADLESS-1", Device: 7}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var n int
	s.display.Do(func() { n = len(s.dmabuf.feedbacks) })
	if n != 0 {
		t.Fatalf("%d feedbacks kept", n)
	}
}

// A v4 client keeps the whole feedback on every round: table, main device,
// and an unflagged renderer tranche.
func TestDMABufFeedbackV4(t *testing.T) {
	tiled := ports.DMABufFormat{Format: linearARGB.Format, Modifier: 0x0200000000000001}
	sup := ports.DMABufSupport{Device: 1, Formats: []ports.DMABufFormat{linearARGB, tiled}}
	h := newScanoutHarness(t, sup, 4)
	check := func(what string, r round, tranches int) {
		t.Helper()
		if r.tables != 1 || r.mainDevices != 1 || len(r.tranches) != tranches || r.tranches[len(r.tranches)-1].flags != 0 {
			t.Fatalf("%s: %+v", what, r)
		}
	}
	check("initial", h.next("initial"), 1)
	h.formats <- ports.OutputFormats{Output: "HEADLESS-1", Device: 7, Formats: []ports.DMABufFormat{tiled}}
	h.commands <- ports.ConfigureWindow{ID: h.w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	r := h.next("fullscreen")
	for len(r.tranches) == 1 { // the formats may land after the configure
		check("fullscreen before formats", r, 1)
		r = h.next("fullscreen with formats")
	}
	check("fullscreen", r, 2)
	if r.tranches[0].flags != uint32(linuxdmabuf.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout) {
		t.Fatalf("scanout tranche: %+v", r)
	}
}

// testDMABufGlobal is a global with one scanout offer for f's output.
func testDMABufGlobal(s *Server, f ports.OutputFormats) *dmabufGlobal {
	g := &dmabufGlobal{
		server:  s,
		support: ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}},
		index:   map[ports.DMABufFormat]uint16{linearARGB: 0},
		scanout: map[string]scanoutOffer{},
	}
	g.scanout[f.Output] = g.offer(f)
	return g
}

func TestFullscreenOutput(t *testing.T) {
	windowSurface := func(mod func(*window)) *surface {
		w := &window{hasLast: true, last: ports.ConfigureWindow{Fullscreen: true, Output: "DP-2", Visible: true}}
		mod(w)
		return &surface{xdg: &xdgSurface{window: w}}
	}
	tests := []struct {
		name string
		surf *surface
		out  string
		ok   bool
	}{
		{"nil", nil, "", false},
		{"destroyed", &surface{destroyed: true, xdg: &xdgSurface{window: &window{hasLast: true, last: ports.ConfigureWindow{Fullscreen: true, Visible: true}}}}, "", false},
		{"no window", &surface{xdg: &xdgSurface{}}, "", false},
		{"not configured", windowSurface(func(w *window) { w.hasLast = false }), "", false},
		{"not fullscreen", windowSurface(func(w *window) { w.last.Fullscreen = false }), "", false},
		{"invisible", windowSurface(func(w *window) { w.last.Visible = false }), "", false},
		{"visible fullscreen", windowSurface(func(*window) {}), "DP-2", true},
	}
	g := testDMABufGlobal(&Server{}, ports.OutputFormats{Output: "DP-2", Formats: []ports.DMABufFormat{linearARGB}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if out, ok := g.fullscreenOutput(tc.surf); out != tc.out || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", out, ok, tc.out, tc.ok)
			}
		})
	}
}

func TestInvisibleScanoutOffer(t *testing.T) {
	w := &window{hasLast: true, last: ports.ConfigureWindow{Fullscreen: true, Output: "DP-2", Visible: true}}
	surf := &surface{xdg: &xdgSurface{window: w}}
	s := &Server{}
	g := testDMABufGlobal(s, ports.OutputFormats{Output: "DP-2", Formats: []ports.DMABufFormat{linearARGB}})
	if _, ok := g.scanoutFor(surf); !ok {
		t.Fatal("no visible scanout")
	}
	w.last.Visible = false
	if _, ok := g.scanoutFor(surf); ok {
		t.Fatal("invisible scanout offered")
	}
	w.last.Visible = true
	if _, ok := g.scanoutFor(surf); !ok {
		t.Fatal("scanout not restored")
	}
}

// A popup's feedback follows its toplevel: resent with it, and on its
// output's format changes.
func TestPopupFeedbackFollowsToplevel(t *testing.T) {
	topWin := &window{hasLast: true, last: ports.ConfigureWindow{Fullscreen: true, Output: "DP-2", Visible: true}}
	top := &surface{xdg: &xdgSurface{window: topWin}}
	topWin.xdg = top.xdg
	top.xdg.surface = top
	popWin := &window{}
	popWin.popup = &popup{w: popWin, parent: topWin}
	pop := &surface{xdg: &xdgSurface{window: popWin}}
	popWin.xdg = pop.xdg
	pop.xdg.surface = pop
	if toplevelRoot(pop) != top {
		t.Fatal("popup does not resolve to its toplevel")
	}
	g := testDMABufGlobal(&Server{}, ports.OutputFormats{Output: "DP-2", Formats: []ports.DMABufFormat{linearARGB}})
	if _, ok := g.scanoutFor(pop); !ok {
		t.Fatal("popup of a visible fullscreen window has no offer")
	}
	topWin.last.Visible = false
	if _, ok := g.scanoutFor(pop); ok {
		t.Fatal("popup of an invisible window keeps its offer")
	}
}
