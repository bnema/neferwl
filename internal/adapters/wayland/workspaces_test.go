package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	ext "github.com/bnema/purego-libwayland/protocol/extworkspace"
	"github.com/bnema/wlturbo"
)

type workspaceEvents struct {
	wlturbo.BaseProxy
	client *wlturbo.Display
	events chan [2]uint32
}

func (p *workspaceEvents) Dispatch(e *wlturbo.Event) {
	v := [2]uint32{uint32(e.Opcode), 0}
	if e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup) || e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspace) {
		v[1] = e.Uint32()
		child := &workspaceChildEvents{events: p.events, group: e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup)}
		child.SetID(v[1])
		p.client.Context().Register(child)
	}
	p.events <- v
}

type workspaceChildEvents struct {
	wlturbo.BaseProxy
	events chan [2]uint32
	group  bool
}

func (p *workspaceChildEvents) Dispatch(e *wlturbo.Event) {
	// Encode the interface in the upper byte to distinguish manager, group and handle.
	kind := uint32(2)
	if p.group {
		kind = 1
	}
	v := [2]uint32{kind<<8 | uint32(e.Opcode), 0}
	switch {
	case p.group && (e.Opcode == uint16(ext.ExtWorkspaceGroupHandleV1EventOutputEnter) || e.Opcode == uint16(ext.ExtWorkspaceGroupHandleV1EventOutputLeave)):
		v[1] = e.Uint32()
	case p.group && (e.Opcode == uint16(ext.ExtWorkspaceGroupHandleV1EventWorkspaceEnter) || e.Opcode == uint16(ext.ExtWorkspaceGroupHandleV1EventWorkspaceLeave)):
		v[1] = e.Uint32()
	case !p.group && (e.Opcode == uint16(ext.ExtWorkspaceHandleV1EventState) || e.Opcode == uint16(ext.ExtWorkspaceHandleV1EventCapabilities)):
		v[1] = e.Uint32()
	}
	p.events <- v
}
func TestWorkspaceProtocol(t *testing.T) {
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	snapshots := make(chan ports.Workspaces, 1)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events, Workspaces: snapshots}, logging.For(context.Background(), "wayland"))
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
	c := protocolClient(t, s, dir)
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Active: true}}}}}
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	managerID := bindProtocol(t, c, "ext_workspace_manager_v1")
	p := &workspaceEvents{client: c, events: make(chan [2]uint32, 128)}
	p.SetID(managerID)
	c.Context().Register(p)
	var handle uint32
	for i := 0; i < 8 && handle == 0; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v[0] == ext.ExtWorkspaceManagerV1EventWorkspace {
				handle = v[1]
			}
		}
	}
	if handle == 0 {
		t.Fatal("workspace not announced")
	}
	out := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, out)
	entered := false
	for i := 0; i < 8 && !entered; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v == [2]uint32{1<<8 | ext.ExtWorkspaceGroupHandleV1EventOutputEnter, out} {
				entered = true
			}
		}
	}
	if !entered {
		t.Fatal("late wl_output did not enter group")
	}
	requestProtocol(t, c, handle, ext.ExtWorkspaceHandleV1RequestActivate)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-events:
		t.Fatalf("activation before commit: %v", v)
	default:
	}
	requestProtocol(t, c, managerID, ext.ExtWorkspaceManagerV1RequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-events:
		if v != (ports.WorkspaceActivate{ID: 42}) {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("no activation")
	}
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Hidden: true}}}}}
	found := false
	for i := 0; i < 8 && !found; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v == [2]uint32{2<<8 | ext.ExtWorkspaceHandleV1EventState, uint32(ext.ExtWorkspaceHandleV1StateHidden)} {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no state change")
	}
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{}}
	found = false
	for i := 0; i < 8 && !found; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v[0] == 1<<8|ext.ExtWorkspaceGroupHandleV1EventRemoved {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("group not removed")
	}
}
