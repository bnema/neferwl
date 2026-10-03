package wayland

import (
	"context"
	"testing"
	"time"

	wlr "github.com/bnema/go-wayland-bindings/server/wlroutputmanagement"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

type managementEvents struct {
	wlturbo.BaseProxy
	events     chan [2]uint32
	client     *wlturbo.Display
	modes      chan uint32
	headEvents chan [2]uint32
}

func (p *managementEvents) Dispatch(e *wlturbo.Event) {
	msg := [2]uint32{uint32(e.Opcode), 0}
	if e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventHead) || e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventDone) {
		msg[1] = e.Uint32()
	}
	if e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventHead) {
		h := &managementHeadEvents{client: p.client, modes: p.modes, events: p.headEvents}
		h.SetID(msg[1])
		registerWireProxy(p.client, h)
	}
	p.events <- msg
}

type managementHeadEvents struct {
	wlturbo.BaseProxy
	client *wlturbo.Display
	modes  chan uint32
	events chan [2]uint32
}

func (p *managementHeadEvents) Dispatch(e *wlturbo.Event) {
	if p.events != nil {
		value := uint32(0)
		switch uint32(e.Opcode) {
		case wlr.ZwlrOutputHeadV1EventEnabled, wlr.ZwlrOutputHeadV1EventCurrentMode, wlr.ZwlrOutputHeadV1EventTransform:
			value = e.Uint32()
		}
		p.events <- [2]uint32{uint32(e.Opcode), value}
	}
	if e.Opcode == uint16(wlr.ZwlrOutputHeadV1EventMode) {
		mode := &protocolProxy{}
		mode.SetID(e.Uint32())
		registerWireProxy(p.client, mode)
		if p.modes != nil {
			p.modes <- mode.ID()
		}
	}
}
func outputTestServer(t *testing.T) (*Server, chan ports.OutputHeads, chan ports.OutputApply, chan ports.OutputApplied, string) {
	t.Helper()
	dir := t.TempDir()
	heads := make(chan ports.OutputHeads, 8)
	applies := make(chan ports.OutputApply, 8)
	replies := make(chan ports.OutputApplied, 8)
	s, err := New(Options{RuntimeDir: dir}, Channels{OutputHeads: heads, OutputApply: applies, OutputApplied: replies}, logging.For(context.Background(), "wayland"))
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
	return s, heads, applies, replies, dir
}
func testHead() ports.OutputHeads {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000, Preferred: true}
	return ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 1920, Height: 1080, RefreshMilli: 60000}, Modes: []ports.OutputMode{mode}, Current: &mode, Enabled: true}}}
}
func recvManagement(t *testing.T, ch <-chan [2]uint32) [2]uint32 {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("management event timeout")
		return [2]uint32{}
	}
}
func waitManagementEvent(t *testing.T, c *wlturbo.Display, ch <-chan [2]uint32, want uint32) {
	t.Helper()
	for i := 0; i < 12; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-ch:
			if ev[0] != want {
				t.Fatalf("event %v want %d", ev, want)
			}
			return
		default:
		}
	}
	t.Fatal("missing management event")
}
func TestOutputManagementStaleSerialAndLayout(t *testing.T) {
	s, _, _, _, dir := outputTestServer(t)
	if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
		t.Fatal("display stopped")
	}
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	manager := &managementEvents{events: make(chan [2]uint32, 32), client: c}
	manager.SetID(id)
	registerWireProxy(c, manager)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var first [2]uint32
	for first[0] != wlr.ZwlrOutputManagerV1EventDone {
		first = recvManagement(t, manager.events)
	}
	if first[0] != wlr.ZwlrOutputManagerV1EventDone {
		t.Fatalf("initial: %v", first)
	}
	if !s.display.Do(func() {
		s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 1920, Height: 1080}, X: 100, Width: 1920, Height: 1080, Scale: 1}}})
	}) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	second := recvManagement(t, manager.events)
	if second[0] != wlr.ZwlrOutputManagerV1EventDone || second[1] == first[1] {
		t.Fatalf("layout serial: %v -> %v", first, second)
	}
	configID := c.AllocateID()
	proxy := &managementEvents{events: make(chan [2]uint32, 8), client: c}
	proxy.SetID(configID)
	registerWireProxy(c, proxy)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, configID, first[1])
	waitManagementEvent(t, c, proxy.events, uint32(wlr.ZwlrOutputConfigurationV1EventCancelled))
	if !s.display.Do(func() { s.setOutputHeads(ports.OutputHeads{}) }) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	third := recvManagement(t, manager.events)
	if third[0] != wlr.ZwlrOutputManagerV1EventDone || third[1] == second[1] {
		t.Fatalf("inventory serial: %v -> %v", second, third)
	}
}

func TestOutputManagementHeadsAndApply(t *testing.T) {
	s, heads, apply, replies, dir := outputTestServer(t)
	heads <- testHead()
	if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
		t.Fatal("display stopped")
	}
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	manager := &managementEvents{events: make(chan [2]uint32, 32), client: c}
	manager.SetID(id)
	registerWireProxy(c, manager)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	head := recvManagement(t, manager.events)
	if head[0] != wlr.ZwlrOutputManagerV1EventHead {
		t.Fatalf("head event: %v", head)
	}
	var serial [2]uint32
	for serial[0] != wlr.ZwlrOutputManagerV1EventDone {
		serial = recvManagement(t, manager.events)
	}
	if serial[1] == 0 {
		t.Fatalf("done: %v", serial)
	}
	configID := c.AllocateID()
	configProxy := &managementEvents{events: make(chan [2]uint32, 8), client: c}
	configProxy.SetID(configID)
	registerWireProxy(c, configProxy)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, configID, serial[1])
	chID := c.AllocateID()
	registerProtocol(t, c, chID)
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestEnableHead, chID, head[1])
	requestProtocol(t, c, chID, wlr.ZwlrOutputConfigurationHeadV1RequestSetPosition, int32(100), int32(0))
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestTest)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var req ports.OutputApply
	select {
	case req = <-apply:
	case <-time.After(time.Second):
		t.Fatal("no test request")
	}
	if !req.Test || len(req.Heads) != 1 || req.Heads[0].Pos.X != 100 {
		t.Fatalf("test: %+v", req)
	}
	replies <- ports.OutputApplied{ID: req.ID}
	waitManagementEvent(t, c, configProxy.events, uint32(wlr.ZwlrOutputConfigurationV1EventSucceeded))
	configID = c.AllocateID()
	configProxy = &managementEvents{events: make(chan [2]uint32, 8), client: c}
	configProxy.SetID(configID)
	registerWireProxy(c, configProxy)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, configID, serial[1])
	chID = c.AllocateID()
	registerProtocol(t, c, chID)
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestEnableHead, chID, head[1])
	requestProtocol(t, c, chID, wlr.ZwlrOutputConfigurationHeadV1RequestSetScale, int32(512))
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestApply)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case req = <-apply:
	case <-time.After(time.Second):
		t.Fatal("no apply request")
	}
	if req.Test || req.Heads[0].Scale != 2 {
		t.Fatalf("apply: %+v", req)
	}
	replies <- ports.OutputApplied{ID: req.ID}
	waitManagementEvent(t, c, configProxy.events, uint32(wlr.ZwlrOutputConfigurationV1EventSucceeded))
}

// Unsupported but well-formed settings fail the configuration, not the client,
// and must never be sent across the app port.
func TestOutputManagementUnsupportedSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*testing.T, *wlturbo.Display, uint32)
	}{
		{"custom mode", func(t *testing.T, c *wlturbo.Display, ch uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetCustomMode, int32(800), int32(600), int32(60000))
		}},
		{"adaptive sync off", func(t *testing.T, c *wlturbo.Display, ch uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetAdaptiveSync, uint32(0))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, apply, _, dir := outputTestServer(t)
			if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
				t.Fatal("display stopped")
			}
			c := protocolClient(t, s, dir)
			managerID := bindVersion(t, c, "zwlr_output_manager_v1", 4)
			manager := &managementEvents{events: make(chan [2]uint32, 32), client: c}
			manager.SetID(managerID)
			registerWireProxy(c, manager)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			head := recvManagement(t, manager.events)
			var done [2]uint32
			for done[0] != wlr.ZwlrOutputManagerV1EventDone {
				done = recvManagement(t, manager.events)
			}
			cfg := c.AllocateID()
			proxy := &managementEvents{events: make(chan [2]uint32, 8), client: c}
			proxy.SetID(cfg)
			registerWireProxy(c, proxy)
			requestProtocol(t, c, managerID, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, done[1])
			ch := c.AllocateID()
			registerProtocol(t, c, ch)
			requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestEnableHead, ch, head[1])
			tc.send(t, c, ch)
			requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestApply)
			waitManagementEvent(t, c, proxy.events, uint32(wlr.ZwlrOutputConfigurationV1EventFailed))
			select {
			case req := <-apply:
				t.Fatalf("unsupported setting reached app: %+v", req)
			default:
			}
		})
	}
}

// Each case uses a new connection because wl_display.error terminates that client.
// set_transform reaches the backend, and heads report the placement's
// transform, at bind time and when it changes.
func TestOutputManagementTransform(t *testing.T) {
	s, _, apply, replies, dir := outputTestServer(t)
	heads := testHead()
	if !s.display.Do(func() {
		s.setOutputHeads(heads)
		s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: heads.Heads[0].Info, Scale: 1, Transform: 1}}})
	}) {
		t.Fatal("display stopped")
	}
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	p := &managementEvents{events: make(chan [2]uint32, 32), headEvents: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
	p.SetID(id)
	registerWireProxy(c, p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var head, serial [2]uint32
	for ev := recvManagement(t, p.events); ; ev = recvManagement(t, p.events) {
		if ev[0] == wlr.ZwlrOutputManagerV1EventHead {
			head = ev
		}
		if ev[0] == wlr.ZwlrOutputManagerV1EventDone {
			serial = ev
			break
		}
	}
	transform := func() (uint32, bool) {
		v, ok := uint32(0), false
		for len(p.headEvents) > 0 {
			if ev := <-p.headEvents; ev[0] == wlr.ZwlrOutputHeadV1EventTransform {
				v, ok = ev[1], true
			}
		}
		return v, ok
	}
	if v, ok := transform(); !ok || v != 1 {
		t.Fatalf("initial transform: %d, %v", v, ok)
	}
	configID := c.AllocateID()
	configProxy := &managementEvents{events: make(chan [2]uint32, 8), client: c}
	configProxy.SetID(configID)
	registerWireProxy(c, configProxy)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, configID, serial[1])
	chID := c.AllocateID()
	registerProtocol(t, c, chID)
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestEnableHead, chID, head[1])
	requestProtocol(t, c, chID, wlr.ZwlrOutputConfigurationHeadV1RequestSetTransform, int32(3))
	requestProtocol(t, c, configID, wlr.ZwlrOutputConfigurationV1RequestApply)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var req ports.OutputApply
	select {
	case req = <-apply:
	case <-time.After(time.Second):
		t.Fatal("no apply request")
	}
	if len(req.Heads) != 1 || req.Heads[0].Transform == nil || *req.Heads[0].Transform != 3 {
		t.Fatalf("apply: %+v", req)
	}
	replies <- ports.OutputApplied{ID: req.ID}
	waitManagementEvent(t, c, configProxy.events, uint32(wlr.ZwlrOutputConfigurationV1EventSucceeded))
	if !s.display.Do(func() {
		s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: heads.Heads[0].Info, Scale: 1, Transform: 3}}})
		s.setOutputHeads(heads)
	}) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if v, ok := transform(); !ok || v != 3 {
		t.Fatalf("updated transform: %d, %v", v, ok)
	}
}

func TestOutputManagementProtocolErrors(t *testing.T) {
	const (
		enable  = wlr.ZwlrOutputConfigurationV1RequestEnableHead
		disable = wlr.ZwlrOutputConfigurationV1RequestDisableHead
		apply   = wlr.ZwlrOutputConfigurationV1RequestApply
		custom  = wlr.ZwlrOutputConfigurationHeadV1RequestSetCustomMode
	)
	cases := []struct {
		name   string
		object string
		code   uint32
		send   func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32)
	}{
		{"already configured (enable)", "configuration", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, cfg, enable, c.AllocateID(), head)
		}},
		{"already configured (disable)", "configuration", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, cfg, disable, head)
		}},
		{"unconfigured head", "configuration", 2, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, cfg, apply)
		}},
		{"already used (configuration)", "configuration", 3, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, cfg, apply)
			requestProtocol(t, c, cfg, apply)
		}},
		{"already used (head)", "configuration", 3, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, cfg, apply)
			requestProtocol(t, c, cfg, enable, c.AllocateID(), head)
		}},
		{"already set mode", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetMode, mode)
			requestProtocol(t, c, ch, custom, int32(800), int32(600), int32(60000))
		}},
		{"already set custom mode", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, custom, int32(800), int32(600), int32(60000))
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetMode, mode)
		}},
		{"already set position", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			for range 2 {
				requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetPosition, int32(0), int32(0))
			}
		}},
		{"already set transform", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			for range 2 {
				requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetTransform, int32(0))
			}
		}},
		{"already set scale", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			for range 2 {
				requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetScale, int32(256))
			}
		}},
		{"already set adaptive sync", "head", 1, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			for range 2 {
				requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetAdaptiveSync, uint32(0))
			}
		}},
		{"invalid mode", "head", 2, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetMode, mode)
		}},
		{"invalid custom mode", "head", 3, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, custom, int32(0), int32(600), int32(60000))
		}},
		{"invalid transform", "head", 4, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetTransform, int32(8))
		}},
		{"invalid scale", "head", 5, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetScale, int32(0))
		}},
		{"invalid adaptive sync state", "head", 6, func(t *testing.T, c *wlturbo.Display, cfg, ch, head, mode uint32) {
			requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetAdaptiveSync, uint32(2))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, dir := outputTestServer(t)
			if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
				t.Fatal("display stopped")
			}
			c := protocolClient(t, s, dir)
			managerID := bindVersion(t, c, "zwlr_output_manager_v1", 4)
			p := &managementEvents{events: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
			p.SetID(managerID)
			registerWireProxy(c, p)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			head := recvManagement(t, p.events)
			if head[0] != wlr.ZwlrOutputManagerV1EventHead {
				t.Fatalf("head: %v", head)
			}
			var done [2]uint32
			for done[0] != wlr.ZwlrOutputManagerV1EventDone {
				done = recvManagement(t, p.events)
			}
			mode := <-p.modes
			if tc.name == "invalid mode" {
				other := bindVersion(t, c, "zwlr_output_manager_v1", 4)
				otherEvents := &managementEvents{events: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
				otherEvents.SetID(other)
				registerWireProxy(c, otherEvents)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				mode = <-otherEvents.modes
			}
			cfg := c.AllocateID()
			registerProtocol(t, c, cfg)
			requestProtocol(t, c, managerID, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, done[1])
			var ch uint32
			if tc.name != "unconfigured head" {
				ch = c.AllocateID()
				registerProtocol(t, c, ch)
				requestProtocol(t, c, cfg, enable, ch, head[1])
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			tc.send(t, c, cfg, ch, head[1], mode)
			object := cfg
			if tc.object == "head" {
				object = ch
			}
			expectProtocolError(t, c, object, tc.code)
		})
	}
}

func TestOutputManagementInitialHeadState(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			s, _, _, _, dir := outputTestServer(t)
			heads := testHead()
			heads.Heads[0].Enabled = enabled
			if !s.display.Do(func() {
				s.setOutputHeads(heads)
				s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: heads.Heads[0].Info, X: 100, Scale: 2}}})
			}) {
				t.Fatal("display stopped")
			}
			c := protocolClient(t, s, dir)
			id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
			p := &managementEvents{events: make(chan [2]uint32, 32), headEvents: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
			p.SetID(id)
			registerWireProxy(c, p)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			for ev := recvManagement(t, p.events); ev[0] != wlr.ZwlrOutputManagerV1EventDone; ev = recvManagement(t, p.events) {
			}
			mode := <-p.modes
			found := map[uint32]bool{}
			for len(p.headEvents) > 0 {
				ev := <-p.headEvents
				found[ev[0]] = true
				if ev[0] == wlr.ZwlrOutputHeadV1EventEnabled && (ev[1] != 0) != enabled {
					t.Fatalf("enabled event: %v", ev)
				}
				if ev[0] == wlr.ZwlrOutputHeadV1EventCurrentMode && ev[1] != mode {
					t.Fatalf("current mode: %v, want %d", ev, mode)
				}
			}
			if !found[wlr.ZwlrOutputHeadV1EventMode] || !found[wlr.ZwlrOutputHeadV1EventEnabled] || found[wlr.ZwlrOutputHeadV1EventCurrentMode] != enabled || found[wlr.ZwlrOutputHeadV1EventTransform] != enabled || found[wlr.ZwlrOutputHeadV1EventPosition] != enabled || found[wlr.ZwlrOutputHeadV1EventScale] != enabled {
				t.Fatalf("head state: %v", found)
			}
		})
	}
}

func TestOutputManagementStopDestroysManager(t *testing.T) {
	s, _, _, _, dir := outputTestServer(t)
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	p := &managementEvents{events: make(chan [2]uint32, 8), client: c}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestStop)
	waitManagementEvent(t, c, p.events, wlr.ZwlrOutputManagerV1EventDone)
	waitManagementEvent(t, c, p.events, wlr.ZwlrOutputManagerV1EventFinished)
	if !s.display.Do(func() {
		if len(s.outputManagers) != 0 {
			t.Errorf("manager retained: %d", len(s.outputManagers))
		}
	}) {
		t.Fatal("display stopped")
	}
}

func TestOutputManagementReleasedModeNotReused(t *testing.T) {
	s, _, _, _, dir := outputTestServer(t)
	if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
		t.Fatal("display stopped")
	}
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	p := &managementEvents{events: make(chan [2]uint32, 32), headEvents: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
	p.SetID(id)
	registerWireProxy(c, p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for ev := recvManagement(t, p.events); ev[0] != wlr.ZwlrOutputManagerV1EventDone; ev = recvManagement(t, p.events) {
	}
	mode := <-p.modes
	for len(p.headEvents) > 0 {
		<-p.headEvents
	}
	requestProtocol(t, c, mode, wlr.ZwlrOutputModeV1RequestRelease)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {
		m := s.outputManagers[0]
		h := m.heads["HEADLESS-1"]
		if len(h.modes) != 0 || len(h.modeResources) != 0 {
			t.Errorf("released mode retained: %d %d", len(h.modes), len(h.modeResources))
		}
		s.setOutputs(ports.SetOutputs{Outputs: ports.Layout{{Info: testHead().Heads[0].Info, X: 100, Scale: 1}}})
	}) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for len(p.headEvents) > 0 {
		if ev := <-p.headEvents; ev[0] == wlr.ZwlrOutputHeadV1EventCurrentMode {
			t.Fatalf("dead mode referenced: %v", ev)
		}
	}
}

func managementSetup(t *testing.T) (*Server, *wlturbo.Display, uint32, uint32, uint32, uint32) {
	t.Helper()
	s, _, _, _, dir := outputTestServer(t)
	if !s.display.Do(func() { s.setOutputHeads(testHead()) }) {
		t.Fatal("display stopped")
	}
	c := protocolClient(t, s, dir)
	id := bindVersion(t, c, "zwlr_output_manager_v1", 4)
	p := &managementEvents{events: make(chan [2]uint32, 32), modes: make(chan uint32, 4), client: c}
	p.SetID(id)
	registerWireProxy(c, p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	head := recvManagement(t, p.events)[1]
	var done [2]uint32
	for done[0] != wlr.ZwlrOutputManagerV1EventDone {
		done = recvManagement(t, p.events)
	}
	return s, c, id, head, <-p.modes, done[1]
}

func TestOutputManagementSetModeAfterHotplug(t *testing.T) {
	s, c, id, head, mode, serial := managementSetup(t)
	cfg := c.AllocateID()
	registerProtocol(t, c, cfg)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, serial)
	ch := c.AllocateID()
	registerProtocol(t, c, ch)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestEnableHead, ch, head)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() { s.setOutputHeads(ports.OutputHeads{}) }) {
		t.Fatal("display stopped")
	}
	requestProtocol(t, c, ch, wlr.ZwlrOutputConfigurationHeadV1RequestSetMode, mode)
	expectProtocolError(t, c, ch, uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidMode))
}

func TestOutputManagementDestroyConfigurationChildren(t *testing.T) {
	s, c, id, head, _, serial := managementSetup(t)
	cfg := c.AllocateID()
	registerProtocol(t, c, cfg)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, serial)
	ch := c.AllocateID()
	registerProtocol(t, c, ch)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestEnableHead, ch, head)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestApply)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var child *wlr.ZwlrOutputConfigurationHeadV1
	if !s.display.Do(func() {
		for _, pending := range s.outputReplies {
			child = pending.heads["HEADLESS-1"].res
		}
	}) {
		t.Fatal("display stopped")
	}
	if child == nil {
		t.Fatal("no child resource")
	}
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestDestroy)
	retireWireObject(c, ch) // the parent destructor also destroys its configuration heads
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {
		if child.Alive() {
			t.Error("configuration head survived parent destruction")
		}
	}) {
		t.Fatal("display stopped")
	}
}

func TestOutputManagementStaleConfigurationSingleTerminal(t *testing.T) {
	_, c, id, _, _, serial := managementSetup(t)
	cfg := c.AllocateID()
	p := &managementEvents{events: make(chan [2]uint32, 8), client: c}
	p.SetID(cfg)
	registerWireProxy(c, p)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, serial-1)
	waitManagementEvent(t, c, p.events, wlr.ZwlrOutputConfigurationV1EventCancelled)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestApply)
	expectProtocolError(t, c, cfg, uint32(wlr.ZwlrOutputConfigurationV1ErrorAlreadyUsed))
	select {
	case ev := <-p.events:
		t.Fatalf("second terminal event: %v", ev)
	default:
	}
}

func TestOutputManagementDestroyDropsPendingReply(t *testing.T) {
	s, c, id, head, _, serial := managementSetup(t)
	cfg := c.AllocateID()
	registerProtocol(t, c, cfg)
	requestProtocol(t, c, id, wlr.ZwlrOutputManagerV1RequestCreateConfiguration, cfg, serial)
	ch := c.AllocateID()
	registerProtocol(t, c, ch)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestEnableHead, ch, head)
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestApply)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {
		if len(s.outputReplies) != 1 {
			t.Errorf("pending replies: %d", len(s.outputReplies))
		}
	}) {
		t.Fatal("display stopped")
	}
	requestProtocol(t, c, cfg, wlr.ZwlrOutputConfigurationV1RequestDestroy)
	retireWireObject(c, ch) // the parent destructor also destroys its configuration heads
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {
		if len(s.outputReplies) != 0 {
			t.Errorf("orphan replies: %d", len(s.outputReplies))
		}
	}) {
		t.Fatal("display stopped")
	}
}
