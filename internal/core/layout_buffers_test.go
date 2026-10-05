package core

import (
	"maps"
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
	m.switchView.off = 0.4
	v["slide"] = m

	// The variants must hold what they are named for, on the workspace on
	// screen (the one whose scratch the tests exercise).
	for name, has := range map[string]func(*Workspace) bool{
		"stash":          func(w *Workspace) bool { return len(w.Stash) > 0 && len(w.Columns) > 0 },
		"floats":         func(w *Workspace) bool { return len(w.Floats) == 2 && len(w.Columns) > 0 },
		"shares":         func(w *Workspace) bool { return validShares(w.Columns[0]) },
		"fixed-expanded": func(w *Workspace) bool { return w.Columns[1].Expanded },
		"fullscreen":     func(w *Workspace) bool { return w.fullscreen == 2 },
		"scroll":         func(w *Workspace) bool { return len(w.Columns) == 5 },
	} {
		if w := v[name].Current(); len(w.Columns) == 0 || !has(w) {
			t.Fatalf("variant %s: the workspace on screen lacks its feature: %+v", name, w.Columns)
		}
	}
	return v
}

// TestLayoutIntoMatchesLayout: the buffer-reusing API gives the same
// placements as the fresh one, whatever the buffer held before (a larger
// stale layout, another monitor's), and repeatedly.
func TestLayoutIntoMatchesLayout(t *testing.T) {
	variants := layoutVariants(t)
	var buf []Placement
	for _, name := range slices.Sorted(maps.Keys(variants)) {
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
		// layoutInto builds in the caller's buffer, never in a scratch of
		// the workspace: with the capacity there it returns that storage.
		w := m.Current()
		big := make([]Placement, 0, 2*len(buf)+8)
		res := w.layoutInto(big)
		if len(res) == 0 || &res[0] != &big[:1][0] {
			t.Fatalf("%s: Workspace.layoutInto did not build in the caller's buffer", name)
		}
		if res = m.layoutInto(big); len(res) == 0 || &res[0] != &big[:1][0] {
			t.Fatalf("%s: Monitor.layoutInto did not build in the caller's buffer", name)
		}
		// A result kept across the calls that use the workspace scratch
		// (zoom tiles, preview tiles, columns, rows, a second layout) is
		// unchanged.
		mine := w.layoutInto(make([]Placement, 0, len(res)))
		mineKept := slices.Clone(mine)
		w.overviewZoom()
		w.peekStep()
		w.previewTiles()
		w.columnRects()
		_ = w.Layout()
		_ = w.layoutInto(nil)
		_ = m.Layout()
		if !slices.Equal(mine, mineKept) {
			t.Fatalf("%s: a kept layoutInto result changed across scratch-using calls\n got %+v\nwant %+v", name, mine, mineKept)
		}
		// Change the layout itself, then lay out again.
		if m.ov.open {
			m.OverviewMove(1, 0)
		} else {
			m.Current().Apply(ActionCycleColumnWidth)
		}
		_ = m.Layout()
		_ = m.Current().Layout()
		m.switchView.off = 0.3
		_ = m.Layout()
		m.switchView.off = 0

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
	// fresh is every screen's layout as a fresh Layout() gives it, taken
	// before a step: the scene it publishes lists the same windows in the
	// same order, and, where nothing moves (no rect motion, no camera), the
	// same rects. A layout scratch shared with the real layouts the overview
	// reads would show here.
	type expect struct {
		out    string
		layout []Placement
		still  bool
	}
	fresh := func(c *Core) []expect {
		var e []expect
		for _, sc := range c.screens {
			if sc.name() != "" {
				e = append(e, expect{sc.name(), sc.mon.Layout(), len(sc.rects) == 0 && !sc.mon.Current().view.motion.on})
			}
		}
		return e
	}
	check := func(set []ports.Scene, want []expect, frame int) {
		t.Helper()
		if len(set) != len(want) {
			t.Fatalf("frame %d: %d scenes, %d screens", frame, len(set), len(want))
		}
		for i, s := range set {
			e := want[i]
			if s.Output != e.out || len(s.Windows) != len(e.layout) {
				t.Fatalf("frame %d: scene %s has %d windows, layout %d", frame, s.Output, len(s.Windows), len(e.layout))
			}
			for k, p := range e.layout {
				w := s.Windows[k]
				if w.ID != p.ID || w.Hidden != p.Hidden || (e.still && w.Rect != p.Rect) {
					t.Fatalf("frame %d: scene %s window %d is %+v, the layout has %+v", frame, s.Output, k, w, p)
				}
			}
		}
	}
	frame := 0
	step := func(c *Core) {
		t.Helper()
		want := fresh(c)
		if err := c.step(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		select {
		case set := <-c.ch.Scenes:
			check(set, want, frame)
			frame++
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
		ws.view.motion = c.spring(viewSpring(100, 0), t0)
		ws.view.off = 100
		startRectMotions(c, sc, t0)
	}
	for range 12 {
		step(c)
	}

	// The overview opening: card motions, the real layouts, separators.
	o := publishRig(t, 1)
	now := time.Now()
	shots := o.snapshot(now)
	o.cur().mon.ToggleOverview()
	o.transition(shots, now)
	for range 12 {
		step(o)
	}
	// The overview settled (no card motion): its scenes list the previews
	// exactly as the monitor lays them out, frame after frame.
	o.cur().stopRects()
	still := 0
	for range 3 {
		if w := fresh(o); !w[0].still {
			t.Fatal("setup: the overview still moves")
		}
		step(o)
		still++
	}
	if !o.cur().mon.ov.open || still == 0 {
		t.Fatal("setup: overview closed")
	}

	if len(all) < 39 {
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

// TestSettledLayoutEmptyFallsBackToLive: an empty layout is nil (as a fresh
// Layout() was), so a screen with nothing published yet measures the live
// layout: a window added after a publish of an empty monitor is listed by
// shownLayout without another publish.
func TestSettledLayoutEmptyFallsBackToLive(t *testing.T) {
	c := publishRig(t, 1)
	sc := c.cur()
	for _, p := range slices.Clone(sc.settledLayout) {
		sc.mon.RemoveWindow(p.ID)
	}
	if err := c.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sc.settledLayout != nil || sc.shown != nil {
		t.Fatalf("empty layout kept: settled %v shown %v (cap %d)", sc.settledLayout, sc.shown, cap(sc.layoutBuf))
	}
	if got := sc.shownLayout(); len(got) != 0 {
		t.Fatalf("empty monitor lists %+v", got)
	}
	sc.mon.AddWindow(77)
	got := sc.shownLayout()
	if len(got) != 1 || got[0].ID != 77 || got[0].Hidden {
		t.Fatalf("the window added after the publish is not listed: %+v", got)
	}
}
