package core

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// layoutVariants builds monitors covering the layout paths that use scratch:
// scroll and fixed overflow, an expanded column, row shares, floats, a stash,
// a fullscreen window, a hidden neighbor workspace, the overview and a slide
// toward a neighbor workspace.
func layoutVariants(t *testing.T) map[string]*Monitor {
	t.Helper()
	base := func(o Overflow, n int) *Monitor {
		m := newMonitor("", "")
		m.SetOutput(300, 200)
		m.SetOverflow(o)
		m.SetGaps(5)
		m.SetMaxColumns(3)
		for id := WindowID(1); id <= WindowID(n); id++ {
			m.AddWindow(id)
		}
		return m
	}
	v := map[string]*Monitor{}
	v["scroll"] = base(OverflowScroll, 5)
	v["fixed"] = base(OverflowFixed, 5)
	v["fixed-spiral"] = base(OverflowFixed, 7)

	m := base(OverflowFixed, 4)
	m.Current().Columns[1].Expanded = true
	v["fixed-expanded"] = m

	m = base(OverflowScroll, 4)
	w := m.Current()
	w.Columns[0].Windows = []WindowID{1, 5, 6}
	w.Columns[0].Shares = []int{50, 30, 20}
	m.AddWindow(5)
	v["shares"] = m

	m = base(OverflowScroll, 4)
	m.AddFloating(10, 100, 80)
	m.AddFloating(11, 60, 40)
	v["floats"] = m

	m = base(OverflowScroll, 4)
	m.Current().Apply(ActionToggleWindowStash)
	m.Current().Apply(ActionToggleWindowStash)
	m.Focus(2)
	m.Current().Apply(ActionToggleWindowStash)
	v["stash"] = m

	m = base(OverflowScroll, 4)
	m.Current().SetFullscreen(2, true)
	v["fullscreen"] = m

	m = base(OverflowScroll, 3)
	m.Apply(ActionFocusWorkspaceDown)
	m.AddWindow(20)
	m.AddWindow(21)
	m.Apply(ActionFocusWorkspaceUp)
	v["two-workspaces"] = m

	m = base(OverflowScroll, 3)
	m.AddFloating(10, 100, 80)
	m.ToggleOverview()
	v["overview"] = m

	m = base(OverflowScroll, 3)
	m.Apply(ActionFocusWorkspaceDown)
	m.AddWindow(20)
	m.Apply(ActionFocusWorkspaceUp)
	m.switchOff = 0.4
	v["slide"] = m
	return v
}

// TestLayoutIntoMatchesLayout: the buffer-reusing API gives the same
// placements as the fresh one, whatever the buffer held before (a larger
// stale layout, another monitor's), and repeatedly.
func TestLayoutIntoMatchesLayout(t *testing.T) {
	variants := layoutVariants(t)
	var buf []Placement
	for _, name := range slices.Sorted(mapsKeys(variants)) {
		m := variants[name]
		want := m.Layout()
		if len(want) == 0 {
			t.Fatalf("%s: empty layout", name)
		}
		for range 3 {
			// Poison the reused buffer: stale entries must not leak.
			for i := range buf[:cap(buf)] {
				buf[:cap(buf)][i] = Placement{ID: 999, Rect: Rect{X: 1, Y: 1, W: 1, H: 1}, Focused: true, Preview: 3}
			}
			buf = m.layoutInto(buf)
			if !slices.Equal(buf, want) {
				t.Fatalf("%s: layoutInto differs\n got %+v\nwant %+v", name, buf, want)
			}
			if again := m.Layout(); !slices.Equal(again, want) {
				t.Fatalf("%s: Layout changed after layoutInto\n got %+v\nwant %+v", name, again, want)
			}
		}
		for w := range m.all() {
			ws := w.Layout()
			if got := w.layoutInto(buf); !slices.Equal(got, ws) {
				t.Fatalf("%s: workspace layoutInto differs", name)
			}
		}
	}
}

func mapsKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// TestLayoutResultsDoNotAlias: Layout() returns a slice the caller owns. A
// retained result stays unchanged after further layouts of the same monitor
// or workspace (nested or later, reused-buffer calls included), and two
// results never share storage.
func TestLayoutResultsDoNotAlias(t *testing.T) {
	for name, m := range layoutVariants(t) {
		first := m.Layout()
		kept := slices.Clone(first)
		wfirst := m.Current().Layout()
		wkept := slices.Clone(wfirst)

		second := m.Layout()
		if &first[0] == &second[0] {
			t.Fatalf("%s: two Layout() calls share storage", name)
		}
		var buf []Placement
		for range 2 {
			buf = m.layoutInto(buf)
			buf = m.Current().layoutInto(buf)
			m.Current().previewTiles()
			m.Current().overviewZoom()
		}
		// Change the layout itself, then lay out again.
		if m.ov.open {
			m.OverviewMove(1, 0)
		} else {
			m.Current().Apply(ActionCycleColumnWidth)
		}
		_ = m.Layout()
		_ = m.Current().Layout()
		m.switchOff = 0.3
		_ = m.Layout()
		m.switchOff = 0

		if !slices.Equal(first, kept) {
			t.Fatalf("%s: a retained Monitor.Layout() changed\n got %+v\nwant %+v", name, first, kept)
		}
		if !slices.Equal(wfirst, wkept) {
			t.Fatalf("%s: a retained Workspace.Layout() changed", name)
		}
	}
}

// TestRectHelpersKeepAppendSemantics: the Into helpers append after dst's
// length and give the same rows as the fresh ones.
func TestRectHelpersKeepAppendSemantics(t *testing.T) {
	r := Rect{X: 3, Y: 4, W: 50, H: 103}
	pre := []Rect{{W: 1}, {W: 2}}
	got := stackRectsInto(slices.Clone(pre), r, 3, 5)
	if !slices.Equal(got[:2], pre) || !slices.Equal(got[2:], stackRects(r, 3, 5)) {
		t.Fatalf("stackRectsInto: %+v", got)
	}
	c := Column{Windows: []WindowID{1, 2, 3}, Shares: []int{50, 30, 20}}
	got = rowRectsInto(slices.Clone(pre), r, c, 5)
	if !slices.Equal(got[:2], pre) || !slices.Equal(got[2:], rowRects(r, c, 5)) {
		t.Fatalf("rowRectsInto: %+v", got)
	}
	// A reused destination overwrites in place.
	buf := make([]Rect, 0, 8)
	a := stackRectsInto(buf, r, 3, 5)
	b := stackRectsInto(buf, r, 2, 5)
	if &a[0] != &b[0] || len(b) != 2 {
		t.Fatalf("not reused: %d %d", len(a), len(b))
	}
}

// cloneScene copies every slice a scene carries.
func cloneScene(s ports.Scene) ports.Scene {
	s.Windows = slices.Clone(s.Windows)
	s.Separators = slices.Clone(s.Separators)
	s.DropHints = slices.Clone(s.DropHints)
	s.Layers = slices.Clone(s.Layers)
	s.CaptureIndicators = slices.Clone(s.CaptureIndicators)
	return s
}

// TestPublishedScenesAreNeverMutated: scenes cross to another goroutine and
// are immutable, so none may alias the layout scratch that the next publish
// rebuilds. Frames run with camera and rect motions, then with the overview
// opening; every earlier scene is compared with its copy at the end. Under
// -race it also catches a later publish writing what a sent scene reads.
func TestPublishedScenesAreNeverMutated(t *testing.T) {
	type kept struct{ sent, copy ports.Scene }
	var all []kept
	take := func(c *Core) {
		t.Helper()
		select {
		case set := <-c.ch.Scenes:
			for _, s := range set {
				all = append(all, kept{s, cloneScene(s)})
			}
		default:
			t.Fatal("no scene published")
		}
	}

	c := publishRig(t, 2)
	t0 := time.Now()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		ws := sc.mon.Current()
		ws.motion = c.spring(viewSpring(100, 0), t0)
		ws.shift = 100
		startRectMotions(c, sc, t0)
	}
	for range 12 {
		if err := c.step(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		take(c)
	}

	// The overview opening: card motions, the real layouts, separators.
	o := publishRig(t, 1)
	now := time.Now()
	shots := o.snapshot(now)
	o.cur().mon.ToggleOverview()
	o.transition(shots, now)
	for range 12 {
		if err := o.step(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		take(o)
	}

	if len(all) < 36 {
		t.Fatalf("only %d scenes", len(all))
	}
	changed := 0
	for i := 1; i < len(all); i++ {
		if !reflect.DeepEqual(all[i].sent, all[i-1].sent) {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("frames never differ: the test would catch nothing")
	}
	for i, k := range all {
		if !reflect.DeepEqual(k.sent, k.copy) {
			t.Fatalf("scene %d (%s) was mutated after it was sent", i, k.sent.Output)
		}
	}
}
