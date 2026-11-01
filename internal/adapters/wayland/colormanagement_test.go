package wayland

import (
	"context"
	"strings"
	"testing"

	"bnema.dev/wlturbo"
	"bnema.dev/zerowrap"
	cm "github.com/bnema/go-wayland-bindings/server/colormanagement"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
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
	registerWireProxy(c, p)
	return p
}
func colorServer(t *testing.T) (*Server, *wlturbo.Display, chan domain.OutputFormats) {
	t.Helper()
	dir := t.TempDir()
	formats := make(chan domain.OutputFormats, 8)
	s, err := New(Options{RuntimeDir: dir, Outputs: domain.Layout{{Info: domain.OutputInfo{Name: "HEADLESS-1", Width: 8, Height: 8}, Width: 8, Height: 8, Scale: 1}}}, Channels{OutputFormats: formats}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
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
	var routed domain.SurfaceColor
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				routed = surf.tree(1).Color
			}
		}
	})
	if routed != (domain.SurfaceColor{TF: domain.ColorTFPQ, Primaries: domain.ColorPrimariesBT2020, MaxCLL: 1000, MaxFALL: 400}) {
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
	out, err := bindWireID(c, name, "wl_output", 4)
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
		s.setOutputHDR(domain.OutputFormats{Output: "HEADLESS-1", HDR: &domain.OutputHDR{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: .005}})
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

// Native clients may query before their first configure, then adapt on feedback.
func TestColorFeedbackConfigureAndLogs(t *testing.T) {
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
	s, _, _, _, dir := contentServerWith(t, Options{}, log)
	c := protocolClient(t, s, dir)
	manager := bindVersion(t, c, "wp_color_manager_v1", 2)
	registerProtocol(t, c, manager)
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	feedback := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurfaceFeedback, feedback, wl)
	changed := watchColor(c, feedback)
	query := func() (*colorEvent, *colorEvent) {
		t.Helper()
		desc := c.AllocateID()
		requestProtocol(t, c, feedback, cm.WpColorManagementSurfaceFeedbackV1RequestGetPreferred, desc)
		ready := watchColor(c, desc)
		info := c.AllocateID()
		requestProtocol(t, c, desc, cm.WpImageDescriptionV1RequestGetInformation, info)
		values := watchColor(c, info)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		return ready, values
	}
	_, before := query()
	if before.values[2][0] != uint32(cm.WpColorManagerV1TransferFunctionSrgb) {
		t.Fatalf("before map: %v", before.values)
	}
	query()
	if n := strings.Count(out.String(), `"message":"preferred color description sent"`); n != 1 {
		t.Fatalf("query logs=%d: %s", n, out.String())
	}
	xdg := c.AllocateID()
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, wl)
	registerProtocol(t, c, xdg)
	top := c.AllocateID()
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	registerProtocol(t, c, top)
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var id domain.WindowID
	s.display.Do(func() {
		for r, surf := range s.surfaces {
			if r.ID() == wl {
				id = surf.toplevelWindow().id
			}
		}
		layout := domain.Layout{s.outputs[0].place}
		layout = append(layout, domain.OutputPlacement{Info: domain.OutputInfo{Name: "HDR", Width: 8, Height: 8}, Width: 8, Height: 8, Scale: 1})
		s.setOutputs(ports.SetOutputs{Outputs: layout})
		s.setOutputHDR(domain.OutputFormats{Output: "HDR", HDR: &domain.OutputHDR{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: .005}})
	})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	changed.events, changed.values = nil, nil
	configure := func(output string) {
		t.Helper()
		s.display.Do(func() { s.apply(ports.ConfigureWindow{ID: id, Output: output, Width: 8, Height: 8, Visible: true}) })
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	configure("HDR")
	if len(changed.events) != 1 {
		t.Fatalf("first assignment feedback: %v id=%d logs=%s", changed.events, id, out.String())
	}
	ready, hdr := query()
	if hdr.values[1][0] != bt2020 || hdr.values[2][0] != pq || hdr.values[5][1] <= hdr.values[3][2] {
		t.Fatalf("HDR info: %v", hdr.values)
	}
	if ready.values[0][0] != changed.values[0][0] || ready.values[0][1] != changed.values[0][1] {
		t.Fatalf("identity ready=%v changed=%v", ready.values, changed.values)
	}
	configure("HDR")
	if len(changed.events) != 1 {
		t.Fatalf("unchanged configure repeated feedback: %v", changed.events)
	}
	configure(testOutputs[0].Info.Name)
	if len(changed.events) != 2 {
		t.Fatalf("move feedback: %v", changed.events)
	}
	_, after := query()
	if after.values[2][0] != uint32(cm.WpColorManagerV1TransferFunctionSrgb) {
		t.Fatalf("after move: %v", after.values)
	}
	control := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurface, control, wl)
	registerProtocol(t, c, control)
	for range 3 {
		requestProtocol(t, c, control, cm.WpColorManagementSurfaceV1RequestUnsetImageDescription)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), `"message":"surface color description unset"`); n != 1 {
		t.Fatalf("unset logs=%d: %s", n, out.String())
	}
	for range 3 {
		requestProtocol(t, c, control, cm.WpColorManagementSurfaceV1RequestSetImageDescription, ready.ID(), uint32(0))
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), `"message":"surface color description set"`); n != 1 {
		t.Fatalf("set logs=%d: %s", n, out.String())
	}
	t.Log(out.String())
}

func TestColorFeedbackBeforeOutputKnown(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindVersion(t, c, "wp_color_manager_v1", 1)
	registerProtocol(t, c, manager)
	comp := bindProtocol(t, c, "wl_compositor")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	feedback := c.AllocateID()
	requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurfaceFeedback, feedback, wl)
	changed := watchColor(c, feedback)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() { s.setOutputs(ports.SetOutputs{}) })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(changed.values) != 1 || changed.values[0][0] == 0 {
		t.Fatalf("unknown output notification: %v", changed.values)
	}
	query := func() *colorEvent {
		t.Helper()
		id := c.AllocateID()
		requestProtocol(t, c, feedback, cm.WpColorManagementSurfaceFeedbackV1RequestGetPreferred, id)
		ready := watchColor(c, id)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		return ready
	}
	first, second := query(), query()
	if first.values[0][0] != second.values[0][0] || first.values[0][0] != changed.values[0][0] {
		t.Fatalf("unstable unknown-output identity: %v %v changed=%v", first.values, second.values, changed.values)
	}
	s.display.Do(func() {
		s.setOutputs(ports.SetOutputs{Outputs: domain.Layout{{Info: domain.OutputInfo{Name: "HDR", Width: 8, Height: 8}, Width: 8, Height: 8, Scale: 1}}, Focused: "HDR"})
		s.setOutputHDR(domain.OutputFormats{Output: "HDR", HDR: &domain.OutputHDR{MaxLuminance: 1000}})
	})
	ready := query()
	if ready.values[0][0] != changed.values[len(changed.values)-1][0] {
		t.Fatalf("first output identity: ready=%v changed=%v", ready.values, changed.values)
	}
	info := c.AllocateID()
	requestProtocol(t, c, ready.ID(), cm.WpImageDescriptionV1RequestGetInformation, info)
	hdr := watchColor(c, info)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if hdr.values[1][0] != bt2020 || hdr.values[2][0] != pq || hdr.values[5][1] <= hdr.values[3][2] {
		t.Fatalf("focused HDR before map: %v", hdr.values)
	}
}
