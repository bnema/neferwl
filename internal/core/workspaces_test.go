package core_test

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWorkspaceChannelCapacity(t *testing.T) {
	_, err := core.New(config.Defaults(), core.Channels{Scenes: make(chan []ports.Scene, 1), Workspaces: make(chan ports.Workspaces, 2)})
	if err == nil {
		t.Fatal("expected capacity-1 validation")
	}
}

func TestWorkspaceSnapshotsAndActivation(t *testing.T) {
	cfg := config.Defaults()
	cfg.Workspaces = append(cfg.Workspaces, ports.WorkspaceConfig{Name: "dev", Hidden: true})
	client := make(chan ports.ClientEvent, 16)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 32)
	snapshots := make(chan ports.Workspaces, 1)
	scenes := make(chan []ports.Scene, 1)
	state := make(chan ports.State, 1)
	spawn := make(chan ports.SpawnRequest, 8)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Workspaces: snapshots, State: state, Scenes: scenes, Spawn: spawn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "A", Width: 100, Height: 80}}
	first := receive(t, snapshots)
	for len(first.Outputs) == 0 {
		first = receive(t, snapshots)
	}
	if len(first.Outputs) != 1 || len(first.Outputs[0].Workspaces) < 2 || !first.Outputs[0].Workspaces[0].Active {
		t.Fatal(first)
	}
	hidden := first.Outputs[0].Workspaces[len(first.Outputs[0].Workspaces)-1]
	if hidden.Configured != "dev" || !hidden.Hidden {
		t.Fatalf("configured hidden workspace: %+v", hidden)
	}
	id := first.Outputs[0].Workspaces[0].ID
	client <- ports.WindowMapped{ID: 1}
	added := receive(t, snapshots)
	if len(added.Outputs[0].Workspaces) < 2 || added.Outputs[0].Workspaces[0].ID != id || !added.Outputs[0].Workspaces[1].Hidden {
		t.Fatal(added)
	}
	spare := added.Outputs[0].Workspaces[1].ID
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "B", Width: 100, Height: 80}}
	two := receive(t, snapshots)
	if len(two.Outputs) != 2 || two.Outputs[0].Workspaces[0].ID != id {
		t.Fatal(two)
	}
	// Activation on B also focuses B, without disturbing A's active workspace.
	bID := two.Outputs[1].Workspaces[0].ID
	client <- ports.WorkspaceActivate{IDs: []uint64{bID}}
	for receive(t, state).Output != "B" {
	}
	// Both requests are applied in order in one event: A wins final focus.
	client <- ports.WorkspaceActivate{IDs: []uint64{bID, spare}}
	for receive(t, state).Output != "A" {
	}
	active := receive(t, snapshots)
	if !active.Outputs[0].Workspaces[1].Active || active.Outputs[1].Workspaces[0].Active != true {
		t.Fatal(active)
	}
	client <- ports.WorkspaceActivate{IDs: []uint64{999999999}}
	scene(t, scenes)
	select {
	case v := <-snapshots:
		t.Fatalf("unknown ID changed snapshot: %v", v)
	default:
	}
	// Move the populated workspace right; its ID follows it.
	client <- ports.WorkspaceActivate{IDs: []uint64{id}}
	receive(t, snapshots)
	input <- ports.KeyEvent{Keysym: "Right", Mods: ports.ModSuper | ports.ModCtrl | ports.ModShift, Pressed: true}
	move := receive(t, snapshots)
	found := false
	for _, w := range move.Outputs[1].Workspaces {
		if w.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("workspace ID lost on move: %+v", move)
	}
	output <- ports.OutputRemoved{Name: "B"}
	moved := receive(t, snapshots)
	if len(moved.Outputs) != 1 || moved.Outputs[0].Name != "A" {
		t.Fatal(moved)
	}
}
