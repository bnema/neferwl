package wayland

import (
	"bnema.dev/wlturbo"
	"encoding/binary"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/xdgshell"
)

func TestInitialFullscreenLastRequestWins(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "unset", true: "set"}[on], func(t *testing.T) {
			s, events, dir := outputSizedServer(t, 800, 600)
			c := protocolClient(t, s, dir)
			mapper := surfaceMapperBeforeMap(t, c, events, 1, func(top uint32) {
				requestProtocol(t, c, top, xdgshell.ToplevelRequestSetFullscreen, uint32(0))
				requestProtocol(t, c, top, xdgshell.ToplevelRequestUnsetFullscreen)
				if on {
					requestProtocol(t, c, top, xdgshell.ToplevelRequestSetFullscreen, uint32(0))
				}
			})
			w, _, _ := mapper()
			if w.Fullscreen != on {
				t.Fatalf("mapped fullscreen=%v, want %v", w.Fullscreen, on)
			}
		})
	}
}

// fullscreenConfigure observes the real xdg_toplevel wire event.
type fullscreenConfigure struct {
	wlturbo.BaseProxy
	configs chan ports.ConfigureWindow
}

func (p *fullscreenConfigure) Dispatch(e *wlturbo.Event) {
	if e.Opcode != uint16(xdgshell.ToplevelEventConfigure) {
		return
	}
	c := ports.ConfigureWindow{Width: int(e.Int32()), Height: int(e.Int32())}
	states := e.Array()
	for len(states) >= 4 {
		c.Fullscreen = c.Fullscreen || binary.LittleEndian.Uint32(states[:4]) == uint32(xdgshell.ToplevelStateFullscreen)
		states = states[4:]
	}
	p.configs <- c
}

//testpolicy:realtime real Wayland socket configure delivery from the server owner loop
func TestInitialFullscreenConfigure(t *testing.T) {
	s, events, commands, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	configs := make(chan ports.ConfigureWindow, 4)
	mapper := surfaceMapperBeforeMap(t, c, events, 1, func(top uint32) {
		p := &fullscreenConfigure{configs: configs}
		p.SetID(top)
		registerWireProxy(c, p)
		requestProtocol(t, c, top, xdgshell.ToplevelRequestSetFullscreen, uint32(0))
	})
	w, _, _ := mapper()
	if !w.Fullscreen {
		t.Fatal("pre-map request lost")
	}
	<-configs // bootstrap configure has no core placement yet
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 800, Height: 600, Fullscreen: w.Fullscreen, Activated: true, Visible: true}
	done := make(chan error, 1)
	go func() {
		for {
			if err := c.Dispatch(); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case got := <-configs:
		if !got.Fullscreen || got.Width != 800 || got.Height != 600 {
			t.Fatalf("configure: %+v", got)
		}
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("missing fullscreen configure")
	}
	// Dispatch may consume just one event; closing the client also releases it.
	_ = c.Close()
	<-done
}
