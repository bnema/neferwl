package wayland

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"github.com/bnema/wlturbo"
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
	mk := func(name string, x int) ports.OutputPlacement {
		return ports.OutputPlacement{Info: ports.OutputInfo{Name: name, Width: 1920, Height: 1080}, X: x, Y: 0, Width: 1920, Height: 1080, Scale: 1}
	}
	return ports.SetOutputs{Outputs: ports.Layout{mk("A", ax), mk("B", 0)}}
}

// Xwayland gets X-space positions (layout top-left is 0,0) and re-receives
// them on a layout change; every other client keeps the layout coordinates.
func TestXwaylandPrivateXDGOutput(t *testing.T) {
	r := newX11Rig(t)
	applyOutputs(t, r.s, r.commands, twoOutputs(-1920))
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
	third.Outputs = append(third.Outputs, ports.OutputPlacement{Info: ports.OutputInfo{Name: "C", Width: 1920, Height: 1080}, X: 1920, Width: 1920, Height: 1080, Scale: 1})
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
