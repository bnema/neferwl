package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// callbackBudgetProxy observes real wl_surface.frame replies.
type callbackBudgetProxy struct {
	wlturbo.BaseProxy
	done chan struct{}
}

func (p *callbackBudgetProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wayland.CallbackEventDone) {
		p.done <- struct{}{}
	}
}

// TestHeadlessTiledSHMPlaybackBudget checks that callbacks continue to drive
// real protocol commits and publications with many stable SHM children.
func TestHeadlessTiledSHMPlaybackBudget(t *testing.T) {
	s, events, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	win, root, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("tiled-playback", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	const tiles = 64
	const edge = 8
	const stride = edge * 4
	const size = (tiles + 1) * edge * stride
	if err := unix.Ftruncate(fd, size); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, pool)
	if err := c.SendRequestWithFDs(shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(size)); err != nil {
		t.Fatal(err)
	}
	children := make([]uint32, tiles+1)
	buffers := make([]uint32, tiles+1)
	for i := range children {
		children[i] = c.AllocateID()
		requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, children[i])
		registerProtocol(t, c, children[i])
		sub := c.AllocateID()
		requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, children[i], root)
		registerProtocol(t, c, sub)
		if i < tiles {
			requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(i%8*edge), int32(i/8*edge))
		} else {
			requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetPosition, int32(80), int32(0))
		}
		buffers[i] = c.AllocateID()
		registerProtocol(t, c, buffers[i])
		requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffers[i], int32(i*edge*stride), int32(edge), int32(edge), int32(stride), uint32(0))
		requestProtocol(t, c, children[i], wayland.SurfaceRequestAttach, buffers[i], int32(0), int32(0))
		requestProtocol(t, c, children[i], wayland.SurfaceRequestCommit)
	}
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	wait := func() ports.SurfaceContent {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-contents:
				if got.ID == win.ID && len(got.Children) == tiles+1 {
					return got
				}
			case <-deadline:
				t.Fatal("no tiled publication")
			}
		}
	}
	previous := wait()
	r, err := vulkan.New(128, 128)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Windows: []ports.SceneWindow{{ID: win.ID, Rect: ports.Rect{W: 128, H: 128}}}}
	render := func(content ports.SurfaceContent) {
		t.Helper()
		fence, err := r.Render(scene, map[ports.WindowID]ports.SurfaceContent{win.ID: content})
		if fence != nil {
			_ = fence.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	render(previous)
	var cpuStart, cpuEnd unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &cpuStart); err != nil {
		t.Fatal(err)
	}
	for i := range 120 {
		done := make(chan struct{}, 1)
		cb := c.AllocateID()
		p := &callbackBudgetProxy{done: done}
		p.SetID(cb)
		c.Context().Register(p)
		requestProtocol(t, c, children[tiles], wayland.SurfaceRequestFrame, cb)
		requestProtocol(t, c, children[tiles], wayland.SurfaceRequestAttach, buffers[tiles], int32(0), int32(0))
		requestProtocol(t, c, children[tiles], wayland.SurfaceRequestCommit)
		requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-done:
				goto callbackDone
			default:
			}
			result := make(chan error, 1)
			go func() { result <- c.Dispatch() }()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-deadline:
				_ = c.Close()
				t.Fatalf("callback %d not delivered", i)
			}
		}
	callbackDone:
		next := wait()
		render(next)
		for tile := range tiles {
			if next.Children[tile].Version != previous.Children[tile].Version {
				t.Fatalf("unchanged tile %d changed version", tile)
			}
		}
		previous = next
	}
	if err := unix.Getrusage(unix.RUSAGE_SELF, &cpuEnd); err != nil {
		t.Fatal(err)
	}
	t.Logf("120 headless callback publications; process user CPU: start=%v end=%v", cpuStart.Utime, cpuEnd.Utime)
}
