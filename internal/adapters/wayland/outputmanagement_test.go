package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	wlr "github.com/bnema/purego-libwayland/protocol/wlroutputmanagement"
	"github.com/bnema/wlturbo"
)

type managementEvents struct {
	wlturbo.BaseProxy
	events chan [2]uint32
	client *wlturbo.Display
	modes  chan uint32
}

func (p *managementEvents) Dispatch(e *wlturbo.Event) {
	msg := [2]uint32{uint32(e.Opcode), 0}
	if e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventHead) || e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventDone) {
		msg[1] = e.Uint32()
	}
	if e.Opcode == uint16(wlr.ZwlrOutputManagerV1EventHead) {
		h := &managementHeadEvents{client: p.client, modes: p.modes}
		h.SetID(msg[1])
		p.client.Context().Register(h)
	}
	p.events <- msg
}

type managementHeadEvents struct {
	wlturbo.BaseProxy
	client *wlturbo.Display
	modes  chan uint32
}

func (p *managementHeadEvents) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wlr.ZwlrOutputHeadV1EventMode) {
		mode := &protocolProxy{}
		mode.SetID(e.Uint32())
		p.client.Context().Register(mode)
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
	c.Context().Register(manager)
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
	c.Context().Register(proxy)
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
	c.Context().Register(manager)
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
	c.Context().Register(configProxy)
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
	c.Context().Register(configProxy)
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

// Each case uses a new connection because wl_display.error terminates that client.
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
			c.Context().Register(p)
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
				c.Context().Register(otherEvents)
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
