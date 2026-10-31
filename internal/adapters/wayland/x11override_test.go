package wayland

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/wayland/xwm"
	"github.com/bnema/neferwl/internal/ports"
)

const ovWin = 0x500002

// overrideWindow maps an override-redirect X window and pairs its surface.
func (r *x11Rig) overrideWindow(win uint32, x, y int16, serial uint64) uint32 {
	r.t.Helper()
	r.peer.createOverride(win, x, y)
	r.peer.mapNotify(win)
	return r.pair(win, serial)
}

func (r *x11Rig) state(f func()) {
	r.t.Helper()
	r.s.display.Do(f)
}

func TestOverrideMapsOnFirstCommitAndMoves(t *testing.T) {
	r := newX11Rig(t)
	r.overrideWindow(ovWin, 30, 40, x11Serial)

	m := awaitEvent[ports.UnmanagedMapped](t, r.events)
	if m.X != 30 || m.Y != 40 || m.Width != 100 || m.Height != 50 || m.NoInput {
		t.Fatalf("mapped %+v", m)
	}
	r.state(func() {
		w := r.s.windows[m.ID]
		if w == nil || !w.isOverride() || w.isToplevel() || !w.mapped {
			t.Errorf("override window %+v", w)
		}
		if surf, _ := r.s.focusTarget(m.ID); surf != nil {
			t.Error("override window is a keyboard focus target")
		}
	})

	r.peer.configureNotify(ovWin, 60, 70, 100, 50)
	if mv := awaitEvent[ports.UnmanagedMoved](t, r.events); mv.ID != m.ID || mv.X != 60 || mv.Y != 70 {
		t.Fatalf("moved %+v", mv)
	}

	r.peer.unmapNotify(ovWin)
	if u := awaitEvent[ports.UnmanagedUnmapped](t, r.events); u.ID != m.ID {
		t.Fatalf("unmapped %+v", u)
	}
	r.state(func() {
		if r.s.windows[m.ID] != nil {
			t.Error("override window kept after unmap")
		}
	})
}

func TestOverrideSurfaceDestroyUnmaps(t *testing.T) {
	r := newX11Rig(t)
	surf := r.overrideWindow(ovWin, 0, 0, x11Serial)
	m := awaitEvent[ports.UnmanagedMapped](t, r.events)
	requestProtocol(t, r.xc, surf, wayland.SurfaceRequestDestroy)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if u := awaitEvent[ports.UnmanagedUnmapped](t, r.events); u.ID != m.ID {
		t.Fatalf("unmapped %+v", u)
	}
}

func TestOverrideTakesPointerAndOutput(t *testing.T) {
	r := newX11Rig(t)
	r.commands <- ports.SetOutputs{Outputs: domain.Layout{
		{Info: domain.OutputInfo{Name: "A"}, X: 0, Y: 0, Width: 1920, Height: 1080, Scale: 1},
		{Info: domain.OutputInfo{Name: "B"}, X: 1920, Y: 0, Width: 1920, Height: 1080, Scale: 1},
	}, Focused: "A"}
	r.overrideWindow(ovWin, 100, 10, x11Serial)
	m := awaitEvent[ports.UnmanagedMapped](t, r.events)
	r.state(func() {
		w := r.s.windows[m.ID]
		if got := r.s.frameOutput(w.x11.surface); got != "A" {
			t.Errorf("frame output %q, want A", got)
		}
		if surf, _, _, _ := r.s.pointerSurface(m.ID, 1, 1); surf == nil {
			t.Error("pointer cannot enter the override surface")
		}
	})
	r.peer.configureNotify(ovWin, 2000, 10, 100, 50)
	awaitEvent[ports.UnmanagedMoved](t, r.events)
	r.state(func() {
		if got := r.s.frameOutput(r.s.windows[m.ID].x11.surface); got != "B" {
			t.Errorf("frame output %q after move, want B", got)
		}
	})
}

func TestOverrideMovedUnchangedAllocations(t *testing.T) {
	s := &Server{windows: map[domain.WindowID]*window{}, x11Slots: map[xwm.Slot]*window{}}
	w := &window{id: 1, x11: &x11Window{override: true, rect: domain.Rect{X: 1, Y: 2, W: 3, H: 4}}}
	s.x11Slots[1] = w
	h := hostImpl{s}
	if n := testing.AllocsPerRun(1000, func() { h.OverrideMoved(1, 1, 2, 3, 4) }); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}
