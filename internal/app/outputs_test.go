package app

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Every output gets the latest cursor, and a new output starts with it.
func TestOutputSetCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set := newOutputSet()
	got := map[string]chan ports.CursorChange{}
	run := func(name string) outputRun {
		got[name] = make(chan ports.CursorChange, 8)
		out := got[name]
		return func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, _ <-chan ports.CaptureRequest) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case c := <-cursor:
					out <- c
				}
			}
		}
	}
	expect := func(name string, want ports.CursorChange) {
		t.Helper()
		select {
		case c := <-got[name]:
			if c.Shape != want.Shape || c.Hidden != want.Hidden {
				t.Fatalf("%s: %+v, want %+v", name, c, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no cursor", name)
		}
	}
	set.start(ctx, "A", run("A"))
	expect("A", ports.CursorChange{})
	set.setCursor(ports.CursorChange{Shape: "text"})
	expect("A", ports.CursorChange{Shape: "text"})
	set.start(ctx, "B", run("B"))
	expect("B", ports.CursorChange{Shape: "text"})
	set.setCursor(ports.CursorChange{Hidden: true})
	expect("A", ports.CursorChange{Hidden: true})
	expect("B", ports.CursorChange{Hidden: true})
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}
