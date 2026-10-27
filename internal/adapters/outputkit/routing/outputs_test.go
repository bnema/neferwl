package routing

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// Every output gets the latest cursor, and a new output starts with it.
func TestOutputSetCursor(t *testing.T) {
	synctest.Test(t, testOutputSetCursor)
}

func testOutputSetCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cursors := make(chan domain.CursorChange)
	set := NewSet(context.Background(), nil, Routes{Cursor: cursors})
	got := map[string]chan domain.CursorChange{}
	run := func(name string) Run {
		got[name] = make(chan domain.CursorChange, 8)
		out := got[name]
		return func(ctx context.Context, feed Feed) error {
			cursor := feed.Cursor
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
	expect := func(name string, want domain.CursorChange) {
		t.Helper()
		synctest.Wait()
		select {
		case c := <-got[name]:
			if c.Shape != want.Shape || c.Hidden != want.Hidden {
				t.Fatalf("%s: %+v, want %+v", name, c, want)
			}
		default:
			t.Fatalf("%s: no cursor", name)
		}
	}
	set.Start(ctx, "A", run("A"))
	expect("A", domain.CursorChange{})
	cursors <- domain.CursorChange{Shape: "text"}
	expect("A", domain.CursorChange{Shape: "text"})
	set.Start(ctx, "B", run("B"))
	expect("B", domain.CursorChange{Shape: "text"})
	cursors <- domain.CursorChange{Hidden: true}
	expect("A", domain.CursorChange{Hidden: true})
	expect("B", domain.CursorChange{Hidden: true})
	if err := set.Wait(); err != nil {
		t.Fatal(err)
	}
}
