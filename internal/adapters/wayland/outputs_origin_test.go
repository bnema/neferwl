package wayland

import (
	"slices"
	"testing"

	"bnema.dev/wlturbo"
)

// outputPositionProxy is a real wire client recording geometry and commit order.
type outputPositionProxy struct {
	wlturbo.BaseProxy
	geometry [2]int32
	logical  [2]int32
	events   *[]string
	xdg      bool
}

func (p *outputPositionProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == 0 {
		pos := [2]int32{e.Int32(), e.Int32()}
		if p.xdg {
			p.logical = pos
			*p.events = append(*p.events, "logical")
		} else {
			p.geometry = pos
			*p.events = append(*p.events, "geometry")
		}
	} else if !p.xdg && e.Opcode == 2 {
		*p.events = append(*p.events, "done")
	}
}

func TestPublicOutputOrigin(t *testing.T) {
	s, _, commands, dir := lifecycleServer(t)
	layout := twoOutputs(3200)
	layout.Outputs[0].Y = 200
	layout.Outputs[0].Primary = true
	applyOutputs(t, s, commands, layout)
	c := protocolClient(t, s, dir)
	manager := bindVersion(t, c, "zxdg_output_manager_v1", 3)
	type binding struct{ wl, x *outputPositionProxy }
	var bindings []binding
	for _, g := range c.Registry().GetGlobals() {
		if g.Interface != "wl_output" {
			continue
		}
		id, err := bindWireID(c, g.Name, g.Interface, 4)
		if err != nil {
			t.Fatal(err)
		}
		events := []string{}
		wl := &outputPositionProxy{events: &events}
		wl.SetID(id)
		registerWireProxy(c, wl)
		x := &outputPositionProxy{xdg: true, events: &events}
		x.SetID(c.AllocateID())
		registerWireProxy(c, x)
		requestProtocol(t, c, manager, 1, x.ID(), id)
		bindings = append(bindings, binding{wl, x})
	}
	roundtrip(t, c)
	// Identify bindings by their first published position, independently of registry order.
	var primary, other binding
	for _, b := range bindings {
		if b.wl.geometry == [2]int32{0, 0} {
			primary = b
		} else {
			other = b
		}
	}
	if primary.wl == nil || other.wl == nil {
		t.Fatal("missing primary or secondary binding")
	}
	check := func(b binding, want [2]int32) {
		t.Helper()
		if b.wl.geometry != want || b.x.logical != want {
			t.Fatalf("geometry %v logical %v want %v", b.wl.geometry, b.x.logical, want)
		}
		events := *b.wl.events
		logical := slices.Index(events, "logical")
		if logical < 0 || slices.Index(events[logical:], "done") < 0 {
			t.Fatalf("logical position not followed by done: %v", events)
		}
		*b.wl.events = nil
	}
	check(primary, [2]int32{0, 0})
	check(other, [2]int32{-3200, -200})
	// Removing the primary flag changes the origin of both existing bindings.
	layout.Outputs[0].Primary = false
	applyOutputs(t, s, commands, layout)
	roundtrip(t, c)
	check(primary, [2]int32{3200, 200})
	check(other, [2]int32{0, 0})
	// Moving a configured primary changes even an otherwise unchanged output.
	layout.Outputs[0].Primary = true
	layout.Outputs[0].X = 4000
	applyOutputs(t, s, commands, layout)
	roundtrip(t, c)
	check(primary, [2]int32{0, 0})
	check(other, [2]int32{-4000, -200})
	// Switching primary without moving either output republishes both bindings.
	layout.Outputs[0].Primary = false
	layout.Outputs[1].Primary = true
	applyOutputs(t, s, commands, layout)
	roundtrip(t, c)
	check(primary, [2]int32{4000, 200})
	check(other, [2]int32{0, 0})
	// No internal coordinates were translated.
	s.display.Do(func() {
		if got := s.outputPlaces; got[0].X != 4000 || got[0].Y != 200 || got[1].X != 0 {
			t.Errorf("internal layout changed: %v", got)
		}
	})
}
