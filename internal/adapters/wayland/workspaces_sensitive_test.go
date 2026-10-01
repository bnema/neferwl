package wayland

import (
	"testing"
	"time"

	ext "github.com/bnema/go-wayland-bindings/server/extworkspace"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
)

// protectedWorkspaceEvents is a real wire client recorder. Child inventory
// proxies decode names as well as opcodes so unlock refresh is verified.
type protectedWorkspaceEvents struct {
	wlturbo.BaseProxy
	client  *wlturbo.Display
	handles []uint32
	names   []string
	events  []uint32
}

func (p *protectedWorkspaceEvents) Dispatch(e *wlturbo.Event) {
	p.events = append(p.events, uint32(e.Opcode))
	if e.Opcode != uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup) && e.Opcode != uint16(ext.ExtWorkspaceManagerV1EventWorkspace) {
		return
	}
	id := e.Uint32()
	group := e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup)
	child := &protectedWorkspaceChildEvents{owner: p, group: group}
	child.SetID(id)
	registerWireProxy(p.client, child)
	if !group {
		p.handles = append(p.handles, id)
	}
}

type protectedWorkspaceChildEvents struct {
	wlturbo.BaseProxy
	owner *protectedWorkspaceEvents
	group bool
}

func (p *protectedWorkspaceChildEvents) Dispatch(e *wlturbo.Event) {
	kind := uint32(2)
	if p.group {
		kind = 1
	}
	p.owner.events = append(p.owner.events, kind<<8|uint32(e.Opcode))
	if !p.group && e.Opcode == uint16(ext.ExtWorkspaceHandleV1EventName) {
		p.owner.names = append(p.owner.names, e.String())
	}
}

func bindProtectedWorkspaceRecorder(t *testing.T, c *wlturbo.Display) *protectedWorkspaceEvents {
	t.Helper()
	p := &protectedWorkspaceEvents{client: c}
	p.SetID(bindProtocol(t, c, "ext_workspace_manager_v1"))
	registerWireProxy(c, p)
	roundtrip(t, c)
	return p
}

func TestProtectedWorkspaceInventoryAndActivation(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	state := installSecurity(t, s)
	snapshot := ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "workspace-before", Active: true}}}}}
	if !s.display.Do(func() { s.updateWorkspaces(snapshot) }) {
		t.Fatal("display stopped")
	}
	existingClient := protocolClient(t, s, dir)
	existing := bindProtectedWorkspaceRecorder(t, existingClient)
	if len(existing.handles) != 1 || len(existing.names) != 1 || existing.names[0] != "workspace-before" {
		t.Fatalf("unlocked inventory: %+v", existing)
	}
	handle := existing.handles[0]
	existing.events, existing.names = nil, nil
	// A pre-protection uncommitted activation is discarded by transition refresh.
	requestProtocol(t, existingClient, handle, ext.ExtWorkspaceHandleV1RequestActivate)
	roundtrip(t, existingClient)
	protect(t, s, state, 1, true)
	if !s.display.Do(func() { s.updateWorkspaceManagers(s.workspaceSnapshot) }) {
		t.Fatal("display stopped")
	}
	snapshot.Outputs[0].Workspaces[0].Name = "workspace-current"
	snapshot.Outputs[0].Workspaces[0].Active = false
	snapshot.Outputs[0].Workspaces[0].Hidden = true
	if !s.display.Do(func() { s.updateWorkspaces(snapshot) }) {
		t.Fatal("display stopped")
	}
	lateClient := protocolClient(t, s, dir)
	late := bindProtectedWorkspaceRecorder(t, lateClient)
	roundtrip(t, existingClient)
	if len(existing.events) != 0 || len(late.events) != 0 || len(late.handles) != 0 {
		t.Fatalf("protected inventory: existing=%v late=%v", existing.events, late.events)
	}
	// Commit cannot admit pre-lock pending requests; requests made while locked
	// must not linger and execute after unlock either.
	requestProtocol(t, existingClient, existing.ID(), ext.ExtWorkspaceManagerV1RequestCommit)
	requestProtocol(t, existingClient, handle, ext.ExtWorkspaceHandleV1RequestActivate)
	roundtrip(t, existingClient)
	select {
	case ev := <-events:
		t.Fatalf("protected activation emitted: %v", ev)
	default:
	}
	protect(t, s, state, 2, false)
	if !s.display.Do(func() { s.updateWorkspaceManagers(s.workspaceSnapshot) }) {
		t.Fatal("display stopped")
	}
	roundtrip(t, existingClient, lateClient)
	for _, p := range []*protectedWorkspaceEvents{existing, late} {
		if len(p.names) != 1 || p.names[0] != "workspace-current" {
			t.Fatalf("unlock names %v", p.names)
		}
	}
	requestProtocol(t, existingClient, existing.ID(), ext.ExtWorkspaceManagerV1RequestCommit)
	roundtrip(t, existingClient)
	select {
	case ev := <-events:
		t.Fatalf("protected activation survived unlock: %v", ev)
	default:
	}
	requestProtocol(t, existingClient, handle, ext.ExtWorkspaceHandleV1RequestActivate)
	requestProtocol(t, existingClient, existing.ID(), ext.ExtWorkspaceManagerV1RequestCommit)
	roundtrip(t, existingClient)
	select {
	case ev := <-events:
		activation, ok := ev.(ports.WorkspaceActivate)
		if !ok || len(activation.IDs) != 1 || activation.IDs[0] != 42 {
			t.Fatalf("unlocked activation: %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unlocked activation not emitted")
	}
}
