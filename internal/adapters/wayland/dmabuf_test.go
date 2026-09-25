package wayland

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
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
