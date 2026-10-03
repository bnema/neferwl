package app

import (
	"context"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOutputRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replies := make(chan ports.CaptureDone, 2)
	set := newOutputSet(ctx, replies)
	for _, name := range []string{"A", "B"} {
		set.start(ctx, name, func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
			<-ctx.Done()
			return nil
		})
		<-set.outs[name].cursor // initial cursor
	}
	set.scenes([]ports.Scene{{Output: "A", Seq: 1}, {Output: "B", Seq: 2}})
	set.scenes([]ports.Scene{{Output: "A", Seq: 3}})
	if got := <-set.outs["A"].scenes; got.Seq != 3 {
		t.Fatalf("A scene: %+v", got)
	}
	if got := <-set.outs["B"].scenes; got.Seq != 2 {
		t.Fatalf("B scene: %+v", got)
	}
	content := ports.SurfaceContent{ID: 1, SHM: &ports.SHMBuffer{}}
	set.content(content)
	for _, name := range []string{"A", "B"} {
		if got := <-set.outs[name].contents; got.ID != content.ID {
			t.Fatalf("%s content: %+v", name, got)
		}
	}
	set.setCursor(ports.CursorChange{Shape: "text"})
	set.setCursor(ports.CursorChange{Shape: "pointer"})
	for _, name := range []string{"A", "B"} {
		if got := <-set.outs[name].cursor; got.Shape != "pointer" {
			t.Fatalf("%s cursor: %+v", name, got)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	set.routeCapture(ports.CaptureRequest{ID: 7, Output: "B", Dst: ports.SHMBuffer{File: f}})
	if got := <-set.outs["B"].captures; got.ID != 7 {
		t.Fatalf("B capture: %+v", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	set.outs["A"].stop()
	if err := set.finish("A"); err != nil {
		t.Fatal(err)
	}
	set.scenes([]ports.Scene{{Output: "A", Seq: 4}})
	if _, ok := set.outs["A"]; ok {
		t.Fatal("removed output retained")
	}
	set.routeCapture(ports.CaptureRequest{ID: 8, Output: "A"})
	if got := <-replies; got.ID != 8 || got.Err == nil {
		t.Fatalf("removed output capture: %+v", got)
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}

// The set remembers which outputs scenes turned off, also once they are
// gone: drm starts an output that reconnects off (Output.StartOff).
func TestOutputSetRemembersOff(t *testing.T) {
	set := newOutputSet(context.Background(), nil)
	set.scenes([]ports.Scene{{Output: "A", Off: true}, {Output: "B"}})
	if !set.off["A"] || set.off["B"] {
		t.Fatal(set.off)
	}
	set.scenes([]ports.Scene{{Output: "A"}})
	if len(set.off) != 0 {
		t.Fatal(set.off)
	}
}

func TestOutputRoutingAllocations(t *testing.T) {
	set := newOutputSet(context.Background(), nil)
	// Populate the existing content slot before measuring, so the guard
	// measures routing rather than first-use map growth.
	content := ports.SurfaceContent{ID: 1, SHM: &ports.SHMBuffer{}}
	set.content(content)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set.start(ctx, "A", func(ctx context.Context, _ <-chan ports.Scene, _ <-chan ports.SurfaceContent, _ <-chan ports.CursorChange, _ <-chan ports.CaptureRequest, _ <-chan ports.SecurityState, _ ports.OutputInstance) error {
		<-ctx.Done()
		return nil
	})
	scenes := []ports.Scene{{Output: "A"}}
	cursor := ports.CursorChange{Shape: "pointer"}
	capture := ports.CaptureRequest{Output: "A"}
	if got := testing.AllocsPerRun(100, func() {
		set.scenes(scenes)
		<-set.outs["A"].contents
		set.content(content)
		set.setCursor(cursor)
		set.routeCapture(capture)
		<-set.outs["A"].captures
	}); got != 0 {
		t.Fatalf("routing: %g allocations per run, want 0", got)
	}
	if err := set.wait(); err != nil {
		t.Fatal(err)
	}
}
