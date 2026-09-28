package wayland

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/linuxdmabuf"
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

// A format the renderer did not list fails; a plane past the end of its
// file is a protocol error.
func TestDMABufParams(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	c := protocolClient(t, s, dir)
	g, ok := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	if !ok || g.Version != dmabufVersion {
		t.Fatalf("global %+v", g)
	}
	dm, err := c.Registry().BindID(g.Name, g.Interface, dmabufVersion)
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
		c.Context().Register(proxy)
		if err := c.SendRequestWithFDs(p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
			t.Fatal(err)
		}
		requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), height, format, uint32(0))
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		return p, proxy.result
	}
	// NV12 is not in the list: failed, not a protocol error.
	_, result := create('N'|'V'<<8|'1'<<16|'2'<<24, 16)
	if op := <-result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventFailed) {
		t.Fatalf("event %d", op)
	}
	// Supported: created (import errors surface in the renderer).
	_, result = create(linearARGB.Format, 16)
	if op := <-result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventCreated) {
		t.Fatalf("event %d", op)
	}
	// Taller than the file: out of bounds.
	big := c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, big)
	if err := c.SendRequestWithFDs(big, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, big, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(17), linearARGB.Format, uint32(0))
	expectProtocolError(t, c, big, uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorOutOfBounds))
}

// A reused params object is a protocol error.
func TestDMABufParamsReuse(t *testing.T) {
	s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}})
	c := protocolClient(t, s, dir)
	g, _ := c.Registry().FindGlobal("zwp_linux_dmabuf_v1")
	dm, err := c.Registry().BindID(g.Name, g.Interface, dmabufVersion)
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
	c.Context().Register(proxy)
	if err := c.SendRequestWithFDs(p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
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

// feedbackProxy records the tranches of each feedback round (done).
type feedbackProxy struct {
	wlturbo.BaseProxy
	rounds chan []tranche
	cur    []tranche
	open   tranche
}

type tranche struct {
	flags   uint32
	indices []uint16
}

func (p *feedbackProxy) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventFormatTable:
		if fd := e.Fd(); fd != 0 {
			unix.Close(int(fd))
		}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheFlags:
		p.open.flags = e.Uint32()
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheFormats:
		a := e.Array()
		for i := 0; i+1 < len(a); i += 2 {
			p.open.indices = append(p.open.indices, uint16(a[i])|uint16(a[i+1])<<8)
		}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventTrancheDone:
		p.cur, p.open = append(p.cur, p.open), tranche{}
	case linuxdmabuf.ZwpLinuxDmabufFeedbackV1EventDone:
		p.rounds <- p.cur
		p.cur = nil
	}
}

// A fullscreen surface on an output with scanout formats gets a scanout
// tranche first; leaving fullscreen sends the renderer tranche alone.
func TestDMABufScanoutTranche(t *testing.T) {
	tiled := ports.DMABufFormat{Format: linearARGB.Format, Modifier: 0x0200000000000001}
	sup := ports.DMABufSupport{Device: 1, Formats: []ports.DMABufFormat{linearARGB, tiled}}
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	formats := make(chan ports.OutputFormats, 1)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, DMABuf: sup}, Channels{Events: events, Commands: commands, OutputFormats: formats}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, c, events)()
	// Our configures must not block the mapper's one-slot proxy.
	registerProtocol(t, c, xdg)
	dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", dmabufVersion)
	fb := c.AllocateID()
	proxy := &feedbackProxy{rounds: make(chan []tranche, 8)}
	proxy.SetID(fb)
	c.Context().Register(proxy)
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestGetSurfaceFeedback, fb, surf)
	round := func(what string) []tranche {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-proxy.rounds:
				return r
			case <-deadline:
				t.Fatalf("no feedback: %s", what)
			default:
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	scanout := uint32(linuxdmabuf.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout)
	if r := round("initial"); len(r) != 1 || r[0].flags != 0 || len(r[0].indices) != 2 {
		t.Fatalf("tiled window: %+v", r)
	}
	formats <- ports.OutputFormats{Output: "HEADLESS-1", Device: 7, Formats: []ports.DMABufFormat{tiled}}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	r := round("fullscreen")
	for len(r) == 1 { // the formats may land after the configure
		r = round("fullscreen with formats")
	}
	if len(r) != 2 || r[0].flags != scanout || len(r[0].indices) != 1 || r[0].indices[0] != 1 || r[1].flags != 0 {
		t.Fatalf("fullscreen: %+v", r)
	}
	// Hidden, the window loses its scanout tranche; shown, it gets it back.
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1"}
	if r := round("hidden"); len(r) != 1 {
		t.Fatalf("hidden fullscreen: %+v", r)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "HEADLESS-1", Visible: true}
	if r := round("shown again"); len(r) != 2 || r[0].flags != scanout {
		t.Fatalf("shown fullscreen: %+v", r)
	}
	// Fullscreen on another output: its formats (none) apply at once.
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 1920, Height: 1080, Fullscreen: true, Output: "OTHER-1", Visible: true}
	if r := round("other output"); len(r) != 1 {
		t.Fatalf("moved output: %+v", r)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 800, Height: 600, Output: "HEADLESS-1", Visible: true}
	if r := round("tiled again"); len(r) != 1 || r[0].flags != 0 {
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

func TestInvisibleScanoutOffer(t *testing.T) {
	w := &window{hasLast: true, last: ports.ConfigureWindow{Fullscreen: true, Output: "DP-2", Visible: true}}
	surf := &surface{xdg: &xdgSurface{window: w}}
	s := &Server{}
	g := &dmabufGlobal{server: s, scanout: map[string]ports.OutputFormats{"DP-2": {Output: "DP-2", Formats: []ports.DMABufFormat{linearARGB}}}}
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
	g := &dmabufGlobal{server: &Server{}, scanout: map[string]ports.OutputFormats{"DP-2": {Output: "DP-2", Formats: []ports.DMABufFormat{linearARGB}}}}
	if _, ok := g.scanoutFor(pop); !ok {
		t.Fatal("popup of a visible fullscreen window has no offer")
	}
	topWin.last.Visible = false
	if _, ok := g.scanoutFor(pop); ok {
		t.Fatal("popup of an invisible window keeps its offer")
	}
}
