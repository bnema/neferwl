package wayland

import (
	"github.com/bnema/neferwl/internal/domain"
	"slices"
	"testing"

	"bnema.dev/purego-libwayland/server"
	"bnema.dev/wlturbo"
	"github.com/bnema/neferwl/internal/ports"
)

// xdgPosProxy records the logical_position events of one zxdg_output_v1.
type xdgPosProxy struct {
	wlturbo.BaseProxy
	pos [][2]int32
}

func (p *xdgPosProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 0 {
		p.pos = append(p.pos, [2]int32{e.Int32(), e.Int32()})
	}
}

// xdgOutputsOf binds the xdg-output manager and an xdg-output for every
// wl_output of c, and returns what each one reported.
func xdgOutputsOf(t *testing.T, c *wlturbo.Display) []*xdgPosProxy {
	t.Helper()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var mgrs int
	var manager uint32
	for _, g := range c.Registry().GetGlobals() {
		if g.Interface == "zxdg_output_manager_v1" {
			mgrs++
			id, err := bindWireID(c, g.Name, g.Interface, 3)
			if err != nil {
				t.Fatal(err)
			}
			registerProtocol(t, c, id)
			manager = id
		}
	}
	if mgrs != 1 {
		t.Fatalf("client sees %d zxdg_output_manager_v1 globals, want 1", mgrs)
	}
	var out []*xdgPosProxy
	for _, g := range c.Registry().GetGlobals() {
		if g.Interface != "wl_output" {
			continue
		}
		wo, err := bindWireID(c, g.Name, g.Interface, 4)
		if err != nil {
			t.Fatal(err)
		}
		xp := &xdgPosProxy{}
		xp.SetID(c.AllocateID())
		registerWireProxy(c, xp)
		requestProtocol(t, c, manager, 1, xp.ID(), wo)
		out = append(out, xp)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return out
}

func lastPositions(ps []*xdgPosProxy) [][2]int32 {
	var r [][2]int32
	for _, p := range ps {
		r = append(r, p.pos[len(p.pos)-1])
	}
	slices.SortFunc(r, func(a, b [2]int32) int { return int(a[0] - b[0]) })
	return r
}

func twoOutputs(ax int) ports.SetOutputs {
	mk := func(name string, x int) domain.OutputPlacement {
		return domain.OutputPlacement{Info: domain.OutputInfo{Name: name, Width: 1920, Height: 1080}, X: x, Y: 0, Width: 1920, Height: 1080, Scale: 1}
	}
	return ports.SetOutputs{Outputs: domain.Layout{mk("A", ax), mk("B", 0)}}
}

// Xwayland gets X-space positions (layout top-left is 0,0) and re-receives
// them on a layout change; every other client keeps the layout coordinates.
func TestXwaylandPrivateXDGOutput(t *testing.T) {
	r := newX11Rig(t)
	initial := twoOutputs(-1920)
	initial.Outputs[1].Primary = true
	applyOutputs(t, r.s, r.commands, initial)
	other := protocolClient(t, r.s, r.dir)
	xo := xdgOutputsOf(t, r.xc)
	oo := xdgOutputsOf(t, other)
	if got, want := lastPositions(xo), [][2]int32{{0, 0}, {1920, 0}}; !slices.Equal(got, want) {
		t.Errorf("Xwayland positions %v, want %v", got, want)
	}
	if got, want := lastPositions(oo), [][2]int32{{-1920, 0}, {0, 0}}; !slices.Equal(got, want) {
		t.Errorf("ordinary positions %v, want %v", got, want)
	}
	applyOutputs(t, r.s, r.commands, twoOutputs(-3840))
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := other.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got, want := lastPositions(xo), [][2]int32{{0, 0}, {3840, 0}}; !slices.Equal(got, want) {
		t.Errorf("Xwayland positions after change %v, want %v", got, want)
	}
	if got, want := lastPositions(oo), [][2]int32{{-3840, 0}, {0, 0}}; !slices.Equal(got, want) {
		t.Errorf("ordinary positions after change %v, want %v", got, want)
	}
	// Hotplug: a third output on the right grows the extents.
	third := twoOutputs(-3840)
	third.Outputs = append(third.Outputs, domain.OutputPlacement{Info: domain.OutputInfo{Name: "C", Width: 1920, Height: 1080}, X: 1920, Width: 1920, Height: 1080, Scale: 1})
	applyOutputs(t, r.s, r.commands, third)
	if err := r.xc.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := lastPositions(xo); len(got) != 2 || got[1] != [2]int32{3840, 0} {
		t.Errorf("Xwayland positions after hotplug %v", got)
	}
}

func TestGlobalFilterAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates in the background")
	}
	s := &Server{xdgPublic: &server.Global{}, xdgPrivate: &server.Global{}}
	if allocs := testing.AllocsPerRun(100, func() {
		s.globalVisible(server.Client{}, s.xdgPrivate)
		s.globalVisible(server.Client{}, s.xdgPublic)
	}); allocs != 0 {
		t.Errorf("globalVisible: %.1f allocs, want 0", allocs)
	}
}

// The X origin is derived by one Space, kept without a window manager.
func TestXSpaceFollowsLayoutWithoutXWM(t *testing.T) {
	r := newX11Rig(t)
	applyOutputs(t, r.s, r.commands, twoOutputs(-1920))
	if ox, oy := r.s.xspace.Origin(); ox != -1920 || oy != 0 {
		t.Fatalf("origin %d,%d, want -1920,0", ox, oy)
	}
}

func TestIsolatedOutputXSpaceExtents(t *testing.T) {
	r := newX11Rig(t)
	outputs := ports.SetOutputs{Outputs: domain.Layout{
		{Info: domain.OutputInfo{Name: "DP-1", Width: 5120, Height: 1440}, Width: 5120, Height: 1440, Scale: 1},
		{Info: domain.OutputInfo{Name: "tv", Virtual: true, Width: 3840, Height: 2160}, X: 5120 + 2024, Width: 3840, Height: 2160, Scale: 1, Isolated: true},
	}}
	applyOutputs(t, r.s, r.commands, outputs)
	positions := lastPositions(xdgOutputsOf(t, r.xc))
	if !slices.Equal(positions, [][2]int32{{0, 0}, {7144, 0}}) {
		t.Fatal(positions)
	}
	r.s.display.Do(func() {
		w, h := r.s.xspace.Size()
		if w != 10984 || h != 2160 || w > 32767 || h > 32767 {
			t.Errorf("X extents %dx%d", w, h)
		}
		if x, y, ok := r.s.xspace.WholeOutput(7144, 0, 3840, 2160); !ok || x != 7144 || y != 0 {
			t.Error("isolated X output unreachable")
		}
	})
}

func TestPrivateXOutputOmitsOverflowingRectangle(t *testing.T) {
	r := newX11Rig(t)
	outputs := ports.SetOutputs{Outputs: domain.Layout{
		{Info: domain.OutputInfo{Name: "DP-1", Width: 5120, Height: 1440}, X: -5120, Y: -100, Width: 5120, Height: 1440, Scale: 1},
		{Info: domain.OutputInfo{Name: "a", Virtual: true, Width: 16384, Height: 16384}, X: 2024, Y: -100, Width: 16384, Height: 16384, Scale: 1, Isolated: true},
		{Info: domain.OutputInfo{Name: "b", Virtual: true, Width: 16384, Height: 16384}, X: 2024, Y: 18308, Width: 16384, Height: 16384, Scale: 1, Isolated: true},
	}}
	applyOutputs(t, r.s, r.commands, outputs)
	private := xdgOutputsOf(t, r.xc)
	var positions [][2]int32
	missing := 0
	for _, p := range private {
		if len(p.pos) == 0 {
			missing++
		} else {
			positions = append(positions, p.pos[len(p.pos)-1])
		}
	}
	slices.SortFunc(positions, func(a, b [2]int32) int { return int(a[0] - b[0]) })
	if missing != 1 || !slices.Equal(positions, [][2]int32{{0, 0}, {7144, 0}}) {
		t.Fatalf("missing=%d positions=%v", missing, positions)
	}
	other := protocolClient(t, r.s, r.dir)
	if public := xdgOutputsOf(t, other); len(public) != 3 {
		t.Fatal("native outputs changed")
	}
}
