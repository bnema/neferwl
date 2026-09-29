package wayland

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	cm "github.com/bnema/purego-libwayland/protocol/colormanagement"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
)

type colorEvent struct {
	wlturbo.BaseProxy
	events []uint16
	values [][]uint32
}

func (p *colorEvent) Dispatch(e *wlturbo.Event) {
	p.events = append(p.events, e.Opcode)
	var vals []uint32
	for i := 0; i < 12; i++ {
		if len(e.Data())-e.Offset() < 4 {
			break
		}
		vals = append(vals, e.Uint32())
	}
	p.values = append(p.values, vals)
}
func watchColor(c *wlturbo.Display, id uint32) *colorEvent {
	p := &colorEvent{}
	p.SetID(id)
	c.Context().Register(p)
	return p
}
func colorServer(t *testing.T) (*Server, *wlturbo.Display, chan ports.OutputFormats) {
	t.Helper()
	dir := t.TempDir()
	formats := make(chan ports.OutputFormats, 8)
	s, err := New(Options{RuntimeDir: dir, Outputs: ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 8, Height: 8}, Width: 8, Height: 8, Scale: 1}}}, Channels{OutputFormats: formats}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s, protocolClient(t, s, dir), formats
}
func TestColorMesaFlow(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindVersion(t, c, "wp_color_manager_v1", 2)
	caps := watchColor(c, manager)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var features, tf, prim []uint32
	for i, op := range caps.events {
		switch uint32(op) {
		case cm.WpColorManagerV1EventSupportedFeature:
			features = append(features, caps.values[i][0])
		case cm.WpColorManagerV1EventSupportedTfNamed:
			tf = append(tf, caps.values[i][0])
		case cm.WpColorManagerV1EventSupportedPrimariesNamed:
			prim = append(prim, caps.values[i][0])
		}
	}
	if len(features) != 1 || features[0] != uint32(cm.WpColorManagerV1FeatureParametric) || len(tf) != 3 || tf[0] != pq || tf[1] != extLinear || tf[2] != uint32(cm.WpColorManagerV1TransferFunctionSrgb) || len(prim) != 2 || uint32(caps.events[len(caps.events)-1]) != cm.WpColorManagerV1EventDone {
		t.Fatalf("capabilities %v %v %v %v", features, tf, prim, caps.events)
	}
	comp := bindProtocol(t, c, "wl_compositor")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	surfaceID := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurface, surfaceID, wl)
	registerProtocol(t, c, surfaceID)
	creator := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestCreateParametricCreator, creator)
	registerProtocol(t, c, creator)
	requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetPrimariesNamed, bt2020)
	requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetTfNamed, pq)
	requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetMaxCll, uint32(1000))
	requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetMaxFall, uint32(400))
	image := c.AllocateID()
	requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestCreate, image)
	ready := watchColor(c, image)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(ready.events) != 1 || uint32(ready.events[0]) != cm.WpImageDescriptionV1EventReady2 {
		t.Fatalf("ready %v", ready.events)
	}
	requestProtocol(t, c, surfaceID, cm.WpColorManagementSurfaceV1RequestSetImageDescription, image, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var before SurfaceColor
	s.display.Do(func() { before = s.surfaceColor(wl) })
	if before.Set {
		t.Fatal("color applied before commit")
	}
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() { before = s.surfaceColor(wl) })
	if before != (SurfaceColor{true, pq, bt2020, 1000, 400}) {
		t.Fatalf("committed color %+v", before)
	}
	var routed ports.SurfaceColor
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				routed = surf.tree(1).Color
			}
		}
	})
	if routed != (ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020, MaxCLL: 1000, MaxFALL: 400}) {
		t.Fatalf("routed color %+v", routed)
	}
	requestProtocol(t, c, surfaceID, cm.WpColorManagementSurfaceV1RequestUnsetImageDescription)
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() { before = s.surfaceColor(wl) })
	if before.Set {
		t.Fatal("unset not committed")
	}
}

// v3 advertises windows_bt2100; its description is PQ BT.2020, applies to
// a surface, and allows no get_information.
func TestColorWindowsBT2100(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindVersion(t, c, "wp_color_manager_v1", 3)
	caps := watchColor(c, manager)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var bt2100 bool
	for i, op := range caps.events {
		if uint32(op) == cm.WpColorManagerV1EventSupportedFeature && caps.values[i][0] == uint32(cm.WpColorManagerV1FeatureWindowsBt2100) {
			bt2100 = true
		}
	}
	if !bt2100 {
		t.Fatalf("windows_bt2100 not advertised: %v", caps.values)
	}
	comp := bindProtocol(t, c, "wl_compositor")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	surfaceID := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurface, surfaceID, wl)
	registerProtocol(t, c, surfaceID)
	image := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestCreateWindowsBt2100, image)
	ready := watchColor(c, image)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(ready.events) != 1 || uint32(ready.events[0]) != cm.WpImageDescriptionV1EventReady2 {
		t.Fatalf("ready %v", ready.events)
	}
	requestProtocol(t, c, surfaceID, cm.WpColorManagementSurfaceV1RequestSetImageDescription, image, uint32(cm.WpColorManagerV1RenderIntentPerceptual))
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var got SurfaceColor
	s.display.Do(func() { got = s.surfaceColor(wl) })
	if got != (SurfaceColor{Set: true, TF: pq, Primaries: bt2020}) {
		t.Fatalf("committed color %+v", got)
	}
	info := c.AllocateID()
	requestProtocol(t, c, image, cm.WpImageDescriptionV1RequestGetInformation, info)
	if err := c.Roundtrip(); err == nil {
		t.Fatal("get_information allowed")
	}
}

func (s *Server) surfaceColor(id uint32) SurfaceColor {
	for r, v := range s.surfaces {
		if r.ID() == id {
			return v.ColorDescription()
		}
	}
	return SurfaceColor{}
}
func TestColorOutputAndFeedback(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindVersion(t, c, "wp_color_manager_v1", 2)
	registerProtocol(t, c, manager)
	var name uint32
	for id, g := range c.Registry().GetGlobals() {
		if g.Interface == "wl_output" {
			name = id
		}
	}
	out, err := c.Registry().BindID(name, "wl_output", 4)
	if err != nil {
		t.Fatal(err)
	}
	registerProtocol(t, c, out)
	obj := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetOutput, obj, out)
	changed := watchColor(c, obj)
	desc := c.AllocateID()
	requestProtocol(t, c, obj, cm.WpColorManagementOutputV1RequestGetImageDescription, desc)
	ready := watchColor(c, desc)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(ready.events) != 1 || uint32(ready.events[0]) != cm.WpImageDescriptionV1EventReady2 {
		t.Fatalf("output ready %v", ready.events)
	}
	oldID := uint64(ready.values[0][0])<<32 | uint64(ready.values[0][1])
	if oldID == 0 {
		t.Fatal("zero output identity")
	}
	info := c.AllocateID()
	requestProtocol(t, c, desc, cm.WpImageDescriptionV1RequestGetInformation, info)
	ev := watchColor(c, info)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(ev.events) < 5 || ev.values[1][0] != srgb || ev.values[2][0] != uint32(cm.WpColorManagerV1TransferFunctionSrgb) {
		t.Fatalf("SDR info %v %v", ev.events, ev.values)
	}
	comp := bindProtocol(t, c, "wl_compositor")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	feedback := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurfaceFeedback, feedback, wl)
	pref := watchColor(c, feedback)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	preferredID := c.AllocateID()
	requestProtocol(t, c, feedback, cm.WpColorManagementSurfaceFeedbackV1RequestGetPreferredParametric, preferredID)
	preferred := watchColor(c, preferredID)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(preferred.events) != 1 || uint32(preferred.events[0]) != cm.WpImageDescriptionV1EventReady2 {
		t.Fatalf("preferred %v", preferred.events)
	}
	s.display.Do(func() {
		s.setOutputHDR(ports.OutputFormats{Output: "HEADLESS-1", HDR: &ports.OutputHDR{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: .005}})
	})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// The format report is asynchronous to client requests.
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(changed.events) == 0 || len(pref.events) == 0 {
		t.Fatalf("change events output=%v preferred=%v", changed.events, pref.events)
	}
	desc2 := c.AllocateID()
	requestProtocol(t, c, obj, cm.WpColorManagementOutputV1RequestGetImageDescription, desc2)
	ready2 := watchColor(c, desc2)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	newID := uint64(ready2.values[0][0])<<32 | uint64(ready2.values[0][1])
	if newID == 0 || newID == oldID {
		t.Fatalf("identities before=%d after=%d", oldID, newID)
	}
	if len(pref.values) == 0 || uint64(pref.values[len(pref.values)-1][0])<<32|uint64(pref.values[len(pref.values)-1][1]) != newID {
		t.Fatalf("preferred identity mismatch: %v, ready=%d", pref.values, newID)
	}
	info2 := c.AllocateID()
	requestProtocol(t, c, desc2, cm.WpImageDescriptionV1RequestGetInformation, info2)
	hdr := watchColor(c, info2)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(hdr.values) < 8 || hdr.values[1][0] != bt2020 || hdr.values[3][0] != 50 || hdr.values[5][0] != 50 || hdr.values[5][1] != 1000 || hdr.values[6][0] != 1000 || hdr.values[7][0] != 400 {
		t.Fatalf("HDR info %v %v", hdr.events, hdr.values)
	}
}
func TestColorParametricCombinations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prim, tf uint32
		ready    bool
	}{
		{"sRGB gamma22", srgb, gamma22, false},
		{"sRGB sRGB", srgb, uint32(cm.WpColorManagerV1TransferFunctionSrgb), true},
		{"BT2020 PQ", bt2020, pq, true},
		{"sRGB extended linear", srgb, extLinear, true},
		{"BT2020 extended linear", bt2020, extLinear, false},
		{"sRGB PQ", srgb, pq, false},
		{"BT2020 gamma22", bt2020, gamma22, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, _ := colorServer(t)
			manager := bindVersion(t, c, "wp_color_manager_v1", 2)
			registerProtocol(t, c, manager)
			creator := c.AllocateID()
			requestProtocol(t, c, manager, cm.WpColorManagerV1RequestCreateParametricCreator, creator)
			registerProtocol(t, c, creator)
			requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetPrimariesNamed, tc.prim)
			requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetTfNamed, tc.tf)
			image := c.AllocateID()
			requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestCreate, image)
			ev := watchColor(c, image)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if len(ev.events) != 1 {
				t.Fatalf("events %v", ev.events)
			}
			if tc.ready {
				if uint32(ev.events[0]) != cm.WpImageDescriptionV1EventReady2 {
					t.Fatalf("events %v", ev.events)
				}
			} else if uint32(ev.events[0]) != cm.WpImageDescriptionV1EventFailed || ev.values[0][0] != uint32(cm.WpImageDescriptionV1CauseUnsupported) {
				t.Fatalf("failure events %v values %v", ev.events, ev.values)
			}
		})
	}
}

func TestColorProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		op       uint32
		args     []any
		code     uint32
		complete bool
	}{
		{"incomplete", cm.WpImageDescriptionCreatorParamsV1RequestCreate, []any{uint32(90)}, 0, false},
		{"invalid tf", cm.WpImageDescriptionCreatorParamsV1RequestSetTfNamed, []any{uint32(99)}, 3, false},
		{"invalid primaries", cm.WpImageDescriptionCreatorParamsV1RequestSetPrimariesNamed, []any{uint32(99)}, 4, false},
		{"unsupported power", cm.WpImageDescriptionCreatorParamsV1RequestSetTfPower, []any{uint32(22000)}, 2, false},
		{"unsupported primaries", cm.WpImageDescriptionCreatorParamsV1RequestSetPrimaries, []any{int32(1), int32(1), int32(1), int32(1), int32(1), int32(1), int32(1), int32(1)}, 2, false},
		{"unsupported luminances", cm.WpImageDescriptionCreatorParamsV1RequestSetLuminances, []any{uint32(1), uint32(100), uint32(100)}, 2, false},
		{"unsupported mastering primaries", cm.WpImageDescriptionCreatorParamsV1RequestSetMasteringDisplayPrimaries, []any{int32(1), int32(1), int32(1), int32(1), int32(1), int32(1), int32(1), int32(1)}, 2, false},
		{"unsupported mastering luminance", cm.WpImageDescriptionCreatorParamsV1RequestSetMasteringLuminance, []any{uint32(1), uint32(100)}, 2, false},
		{"already tf", cm.WpImageDescriptionCreatorParamsV1RequestSetTfNamed, []any{pq}, 1, true},
		{"already primaries", cm.WpImageDescriptionCreatorParamsV1RequestSetPrimariesNamed, []any{bt2020}, 1, true},
		{"max fall", cm.WpImageDescriptionCreatorParamsV1RequestCreate, []any{uint32(90)}, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, _ := colorServer(t)
			manager := bindVersion(t, c, "wp_color_manager_v1", 2)
			registerProtocol(t, c, manager)
			creator := c.AllocateID()
			requestProtocol(t, c, manager, cm.WpColorManagerV1RequestCreateParametricCreator, creator)
			registerProtocol(t, c, creator)
			if tc.complete {
				requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetTfNamed, pq)
				requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetPrimariesNamed, bt2020)
				if tc.name == "max fall" {
					requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetMaxCll, uint32(100))
					requestProtocol(t, c, creator, cm.WpImageDescriptionCreatorParamsV1RequestSetMaxFall, uint32(200))
				}
			}
			args := tc.args
			if tc.op == cm.WpImageDescriptionCreatorParamsV1RequestCreate {
				args = []any{c.AllocateID()}
			}
			requestProtocol(t, c, creator, tc.op, args...)
			expectProtocolError(t, c, creator, tc.code)
		})
	}
}
