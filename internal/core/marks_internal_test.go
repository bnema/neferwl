package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Marks are unique across windows, toggle and clear on the record, and go
// with the window when it unmaps.
func TestMarkStorage(t *testing.T) {
	var cfg domain.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c := newSizedCore(t, cfg)
	c.ch.Commands = make(chan ports.ClientCommand, 256)
	c.addScreen(domain.OutputInfo{Name: "A", Width: 200, Height: 100})
	ctx := context.Background()
	c.clientEvent(ctx, ports.WindowMapped{ID: 1})
	c.clientEvent(ctx, ports.WindowMapped{ID: 2})
	marks := func(id WindowID) domain.MarkSet { return c.windows.lookup(id).marks }

	if c.setMark(99, "a") || c.toggleMark(99, "a") || c.unmarkAll(99) {
		t.Fatal("unknown window changed")
	}
	if !c.setMark(1, "a") || c.markHolder("a") != 1 {
		t.Fatal("setMark on 1")
	}
	if c.setMark(1, "a") {
		t.Fatal("setMark again reported a change")
	}
	if !c.setMark(2, "a") || c.markHolder("a") != 2 || marks(1) != "" {
		t.Fatalf("mark did not move: 1=%q 2=%q", marks(1), marks(2))
	}
	if !c.toggleMark(1, "b") || marks(1) != "b" {
		t.Fatalf("toggle add: %q", marks(1))
	}
	if !c.toggleMark(1, "b") || marks(1) != "" {
		t.Fatalf("toggle remove: %q", marks(1))
	}
	c.setMark(2, "c")
	if marks(2) != "a c" {
		t.Fatalf("marks %q", marks(2))
	}
	if !c.unmarkName("c") || c.unmarkName("c") || marks(2) != "a" {
		t.Fatalf("unmarkName: %q", marks(2))
	}
	c.setMark(1, "z")
	if !c.unmarkAll(1) || marks(1) != "" || c.unmarkAll(1) {
		t.Fatal("unmarkAll")
	}
	c.clientEvent(ctx, ports.WindowUnmapped{ID: 2})
	if c.markHolder("a") != 0 {
		t.Fatal("mark kept after unmap")
	}
}
