package core

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// While an exclusion is live, the set of layers kept over a fullscreen window
// is built once per change of the attached set and shared by every screen: a
// publish allocates nothing for it.
func TestExclusionKeepIsSharedAndBuiltOncePerChange(t *testing.T) {
	c, _, _ := hiddenCaptureCommands(t)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 100, Height: 100})
	c.captureOpen(ports.CaptureSessionOpen{ID: 1, Output: "A"})
	c.captureExclusionBegin(ports.CaptureExclusionBegin{Session: 1})
	c.captureExclusionLayer(ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true})
	c.captureExclusionLayer(ports.CaptureExclusionLayer{Session: 1, Layer: 11, Attached: true})
	e := c.capExcl
	keep := c.exclusionKeep(e)
	if len(keep) != 2 || !keep[10] || !keep[11] {
		t.Fatalf("keep %v", keep)
	}
	if n := testing.AllocsPerRun(100, func() { _ = c.exclusionKeep(e) }); n != 0 {
		t.Fatalf("%v allocations for an unchanged exclusion", n)
	}
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, sc := range c.screens {
		if reflect.ValueOf(sc.capture).Pointer() != reflect.ValueOf(keep).Pointer() {
			t.Fatalf("screen %s has its own keep set", sc.name())
		}
	}
	// A change of the attached set builds a new one, once.
	c.captureExclusionLayer(ports.CaptureExclusionLayer{Session: 1, Layer: 11})
	if k := c.exclusionKeep(e); len(k) != 1 || !k[10] {
		t.Fatalf("keep after detach %v", k)
	}
	// An ended exclusion keeps nothing over a fullscreen window.
	c.screens[0].layers = []ports.LayerSurface{{ID: 10}}
	c.captureExclusionEnd(1)
	if c.capExcl == nil || !c.capExcl.ended || !slices.Equal(c.capExcl.retained, []WindowID{10}) {
		t.Fatalf("ended exclusion %+v", c.capExcl)
	}
	if k := c.exclusionKeep(c.capExcl); k != nil {
		t.Fatalf("keep of an ended exclusion %v", k)
	}
	// With no layer listed any more it is gone.
	c.screens[0].layers = nil
	c.pruneRetained()
	if c.capExcl != nil {
		t.Fatalf("exclusion survived its last listed layer: %+v", c.capExcl)
	}
}
