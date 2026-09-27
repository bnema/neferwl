package wayland

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	ext "github.com/bnema/purego-libwayland/protocol/extworkspace"
	"github.com/bnema/wlturbo"
)

// workspaceEventOrder records dispatch order across manager and child objects.
type workspaceEventOrder struct {
	mu     sync.Mutex
	events []uint32
}

func (o *workspaceEventOrder) add(v uint32) {
	if o != nil {
		o.mu.Lock()
		o.events = append(o.events, v)
		o.mu.Unlock()
	}
}
func (o *workspaceEventOrder) take() []uint32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := slices.Clone(o.events)
	o.events = nil
	return v
}

type workspaceEvents struct {
	wlturbo.BaseProxy
	client   *wlturbo.Display
	events   chan [2]uint32
	idEvents chan string
	order    *workspaceEventOrder
}

func (p *workspaceEvents) Dispatch(e *wlturbo.Event) {
	v := [2]uint32{uint32(e.Opcode), 0}
	if e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup) || e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspace) {
		v[1] = e.Uint32()
		child := &workspaceChildEvents{events: p.events, idEvents: p.idEvents, order: p.order, group: e.Opcode == uint16(ext.ExtWorkspaceManagerV1EventWorkspaceGroup)}
		child.SetID(v[1])
		p.client.Context().Register(child)
	}
	p.order.add(v[0])
	p.events <- v
}

type workspaceChildEvents struct {
	wlturbo.BaseProxy
	events   chan [2]uint32
	group    bool
	idEvents chan string
	order    *workspaceEventOrder
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
	case !p.group && e.Opcode == uint16(ext.ExtWorkspaceHandleV1EventId):
		p.idEvents <- e.String()
	}
	p.order.add(v[0])
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
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Active: true}, {ID: 43, Name: "2", Index: 1}}}}}
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	managerID := bindProtocol(t, c, "ext_workspace_manager_v1")
	p := &workspaceEvents{client: c, events: make(chan [2]uint32, 128), idEvents: make(chan string, 16)}
	p.SetID(managerID)
	c.Context().Register(p)
	var handle, secondHandle uint32
	for i := 0; i < 8 && secondHandle == 0; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v[0] == ext.ExtWorkspaceManagerV1EventWorkspace {
				if handle == 0 {
					handle = v[1]
				} else {
					secondHandle = v[1]
				}
			}
		}
	}
	if handle == 0 {
		t.Fatal("workspace not announced")
	}
	select {
	case id := <-p.idEvents:
		t.Fatalf("dynamic workspace sent stable id %q", id)
	default:
	}
	out := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, out)
	entered := false
	doneCount := 0
	for i := 0; i < 8 && !entered; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v == [2]uint32{1<<8 | ext.ExtWorkspaceGroupHandleV1EventOutputEnter, out} {
				entered = true
			}
			if v[0] == ext.ExtWorkspaceManagerV1EventDone {
				doneCount++
			}
		}
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for len(p.events) > 0 {
		if (<-p.events)[0] == ext.ExtWorkspaceManagerV1EventDone {
			doneCount++
		}
	}
	if !entered || doneCount != 1 {
		t.Fatalf("late output enter=%v, done=%d", entered, doneCount)
	}
	if len(p.idEvents) != 0 {
		t.Fatal("dynamic workspace sent id")
	}
	requestProtocol(t, c, handle, ext.ExtWorkspaceHandleV1RequestActivate)
	requestProtocol(t, c, secondHandle, ext.ExtWorkspaceHandleV1RequestActivate)
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
		if got, ok := v.(ports.WorkspaceActivate); !ok || !slices.Equal(got.IDs, []uint64{42, 43}) {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("no activation")
	}
	select {
	case v := <-events:
		t.Fatalf("extra activation message: %v", v)
	default:
	}
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Hidden: true}, {ID: 43, Name: "2", Index: 1}}}}}
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

func TestWorkspaceConfiguredIDAndBatchDone(t *testing.T) {
	dir := t.TempDir()
	snapshots := make(chan ports.Workspaces, 1)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Workspaces: snapshots}, logging.For(context.Background(), "wayland"))
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
	// Bind manager first; the initial two-output batch has exactly one done.
	managerID := bindProtocol(t, c, "ext_workspace_manager_v1")
	p := &workspaceEvents{client: c, events: make(chan [2]uint32, 128), idEvents: make(chan string, 16), order: &workspaceEventOrder{}}
	p.SetID(managerID)
	c.Context().Register(p)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for len(p.events) > 0 {
		<-p.events
	}
	p.order.take()
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{
		{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Active: true}, {ID: 43, Name: "dev", Configured: "dev", Index: 1, Hidden: true}}},
		{Name: "HEADLESS-2", Workspaces: []ports.WorkspaceInfo{{ID: 44, Name: "1", Active: true}}},
	}}
	// Synchronize with the display owner's processing of the channel snapshot.
	for i := 0; i < 8; i++ {
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if len(p.events) > 0 {
			break
		}
	}
	doneCount := 0
	for len(p.events) > 0 {
		if (<-p.events)[0] == ext.ExtWorkspaceManagerV1EventDone {
			doneCount++
		}
	}
	if doneCount != 1 {
		t.Fatalf("snapshot done=%d, want 1", doneCount)
	}
	order := p.order.take()
	// The second group begins after the first group's workspaces are fully
	// initialized; workspace_enter follows all initial workspace details.
	wantOrder := []uint32{
		ext.ExtWorkspaceManagerV1EventWorkspaceGroup, 1<<8 | ext.ExtWorkspaceGroupHandleV1EventCapabilities,
		ext.ExtWorkspaceManagerV1EventWorkspace, 2<<8 | ext.ExtWorkspaceHandleV1EventName, 2<<8 | ext.ExtWorkspaceHandleV1EventCoordinates, 2<<8 | ext.ExtWorkspaceHandleV1EventState, 2<<8 | ext.ExtWorkspaceHandleV1EventCapabilities, 1<<8 | ext.ExtWorkspaceGroupHandleV1EventWorkspaceEnter,
		ext.ExtWorkspaceManagerV1EventWorkspace, 2<<8 | ext.ExtWorkspaceHandleV1EventId, 2<<8 | ext.ExtWorkspaceHandleV1EventName, 2<<8 | ext.ExtWorkspaceHandleV1EventCoordinates, 2<<8 | ext.ExtWorkspaceHandleV1EventState, 2<<8 | ext.ExtWorkspaceHandleV1EventCapabilities, 1<<8 | ext.ExtWorkspaceGroupHandleV1EventWorkspaceEnter,
		ext.ExtWorkspaceManagerV1EventWorkspaceGroup, 1<<8 | ext.ExtWorkspaceGroupHandleV1EventCapabilities,
		ext.ExtWorkspaceManagerV1EventWorkspace, 2<<8 | ext.ExtWorkspaceHandleV1EventName, 2<<8 | ext.ExtWorkspaceHandleV1EventCoordinates, 2<<8 | ext.ExtWorkspaceHandleV1EventState, 2<<8 | ext.ExtWorkspaceHandleV1EventCapabilities, 1<<8 | ext.ExtWorkspaceGroupHandleV1EventWorkspaceEnter,
		ext.ExtWorkspaceManagerV1EventDone,
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("creation order = %v, want %v", order, wantOrder)
	}
	select {
	case id := <-p.idEvents:
		if id != "dev" {
			t.Fatalf("id=%q", id)
		}
	default:
		t.Fatal("configured workspace has no id")
	}
	if len(p.idEvents) != 0 {
		t.Fatal("dynamic workspace received id")
	}
	out := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, out)
	entered, outputDone := 0, 0
	for i := 0; i < 8 && outputDone == 0; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v == [2]uint32{1<<8 | ext.ExtWorkspaceGroupHandleV1EventOutputEnter, out} {
				entered++
			}
			if v[0] == ext.ExtWorkspaceManagerV1EventDone {
				outputDone++
			}
		}
	}
	if entered != 1 || outputDone != 1 {
		t.Fatalf("late output enter=%d done=%d", entered, outputDone)
	}
	second := bindProtocol(t, c, "wl_output")
	registerProtocol(t, c, second)
	entered, outputDone = 0, 0
	for i := 0; i < 8 && outputDone == 0; i++ {
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v == [2]uint32{1<<8 | ext.ExtWorkspaceGroupHandleV1EventOutputEnter, second} {
				entered++
			}
			if v[0] == ext.ExtWorkspaceManagerV1EventDone {
				outputDone++
			}
		}
	}
	if entered != 1 || outputDone != 1 {
		t.Fatalf("second output enter=%d done=%d", entered, outputDone)
	}
	// A client may destroy a handle without removing the underlying workspace.
	// A subsequent snapshot must not recreate the destroyed protocol object.
	var group, workspace uint32
	// Inspect server resources on the display owner's goroutine.
	if !s.display.Do(func() {
		m := s.workspaceManagers[0]
		group = m.groups["HEADLESS-1"].res.ID()
		workspace = m.handles[42].res.ID()
	}) {
		t.Fatal("display stopped")
	}
	requestProtocol(t, c, workspace, ext.ExtWorkspaceHandleV1RequestDestroy)
	requestProtocol(t, c, group, ext.ExtWorkspaceGroupHandleV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "one", Active: true}, {ID: 43, Name: "dev", Configured: "dev", Index: 1, Hidden: true}}}, {Name: "HEADLESS-2", Workspaces: []ports.WorkspaceInfo{{ID: 44, Name: "1", Active: true}}}}}
	for i := 0; i < 8; i++ {
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	for len(p.events) > 0 {
		v := <-p.events
		if v[0] == ext.ExtWorkspaceManagerV1EventWorkspace || v[0] == ext.ExtWorkspaceManagerV1EventWorkspaceGroup {
			t.Fatalf("recreated client-destroyed handle: %v", v)
		}
	}
	// A configured rename replaces the handle on the still-live second group.
	// The destroyed first group remains a tombstone until it disappears.
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "one", Active: true}, {ID: 43, Name: "dev", Configured: "dev", Index: 1, Hidden: true}}}, {Name: "HEADLESS-2", Workspaces: []ports.WorkspaceInfo{{ID: 44, Name: "code", Configured: "code", Active: true}}}}}
	replaced, removed := 0, 0
	for i := 0; i < 8 && replaced == 0; i++ {
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v[0] == ext.ExtWorkspaceManagerV1EventWorkspace {
				replaced++
			}
			if v[0] == 2<<8|ext.ExtWorkspaceHandleV1EventRemoved {
				removed++
			}
		}
	}
	if replaced != 1 || removed != 1 {
		t.Fatalf("rename replaced=%d removed=%d", replaced, removed)
	}
	select {
	case id := <-p.idEvents:
		if id != "code" {
			t.Fatalf("renamed id=%q", id)
		}
	default:
		t.Fatal("missing renamed id")
	}
	if len(p.idEvents) != 0 {
		t.Fatal("extra id")
	}
	// Removing the group clears its tombstone, allowing a new handle on return.
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{}}
	for i := 0; i < 8; i++ {
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	for len(p.events) > 0 {
		<-p.events
	}
	snapshots <- ports.Workspaces{Outputs: []ports.WorkspaceOutput{{Name: "HEADLESS-1", Workspaces: []ports.WorkspaceInfo{{ID: 42, Name: "1", Active: true}}}}}
	recreated := 0
	for i := 0; i < 8 && recreated < 2; i++ {
		if !s.display.Do(func() {}) {
			t.Fatal("display stopped")
		}
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for len(p.events) > 0 {
			v := <-p.events
			if v[0] == ext.ExtWorkspaceManagerV1EventWorkspace || v[0] == ext.ExtWorkspaceManagerV1EventWorkspaceGroup {
				recreated++
			}
		}
	}
	if recreated != 2 {
		t.Fatalf("new group and workspace after disappearance: %d", recreated)
	}
}
