package core

import (
	"context"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestToggleWindowStash(t *testing.T) {
	for _, stacked := range []bool{false, true} {
		t.Run(map[bool]string{false: "column", true: "stack"}[stacked], func(t *testing.T) {
			w := workspace()
			w.border = 2
			w.AddWindow(1)
			w.AddWindow(2)
			w.Columns[1].Width = Width{Pixels: 35}
			if stacked {
				w.Columns[1].Windows = []WindowID{3, 2}
				w.Columns[1].Focus = 1
			}
			w.Apply(ActionToggleWindowStash)
			if len(w.Stash) != 1 || len(w.Floats) != 0 || !w.Stash[0].back.holdsStack(Column{Windows: []WindowID{3}}) && stacked {
				t.Fatalf("stash %+v", w.Stash)
			}
			p := placement(w, 2)
			if !p.Focused || !p.Floating || p.Rect != (Rect{X: 10, Y: 8, W: 80, H: 64}) {
				t.Fatalf("placement %+v", p)
			}
			w.ResizeFloating(2, 12, 14)
			if placement(w, 2).Rect != p.Rect {
				t.Fatal("client resize moved stashed window")
			}
			w.Apply(ActionToggleWindowStash)
			id, _ := w.Focused()
			if id != 2 || len(w.Stash) != 0 || w.Focus != 1 || w.Columns[1].Width != (Width{Pixels: 35}) || !slices.Contains(w.Columns[1].Windows, 2) {
				t.Fatalf("round trip: %+v", w)
			}
		})
	}
	w := workspace()
	w.AddFloating(4, 20, 10)
	w.Apply(ActionToggleWindowStash)
	if len(w.Floats) != 0 || len(w.Columns) != 1 || w.Columns[0].Windows[0] != 4 {
		t.Fatalf("native float: %+v", w)
	}
}

// placement returns the placement of id in the workspace layout.
func placement(w *Workspace, id WindowID) Placement {
	for _, p := range w.Layout() {
		if p.ID == id {
			return p
		}
	}
	return Placement{}
}

// The stash is a strip: new windows go last, selected and centred; the
// neighbors peek in beside it, stash.gap away; focus moves inside it and stops at its
// ends; up and down do nothing there.
func TestStashStrip(t *testing.T) {
	w := workspace()
	w.stashGap = 5
	for id := WindowID(1); id <= 4; id++ {
		w.AddWindow(id)
	}
	for _, id := range []WindowID{4, 3, 2} {
		w.FocusID(id)
		w.Apply(ActionToggleWindowStash)
	}
	if got := []WindowID{w.Stash[0].ID, w.Stash[1].ID, w.Stash[2].ID}; !slices.Equal(got, []WindowID{4, 3, 2}) {
		t.Fatalf("order %v", got)
	}
	if id, _ := w.Focused(); id != 2 || w.stashAt != 2 {
		t.Fatalf("focused %d at %d", id, w.stashAt)
	}
	center := Rect{X: 10, Y: 8, W: 80, H: 64}
	if p := placement(w, 2); p.Rect != center || p.Peek || p.Hidden {
		t.Fatalf("selected %+v", p)
	}
	if p := placement(w, 3); !p.Peek || p.Rect != (Rect{X: 5 - 80, Y: 8, W: 80, H: 64}) {
		t.Fatalf("left peek %+v", p)
	}
	if p := placement(w, 4); !p.Hidden {
		t.Fatalf("far window %+v", p)
	}
	w.Apply(ActionFocusColumnRight)
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("moved past the end: %d", id)
	}
	w.Apply(ActionFocusColumnLeft)
	if p := placement(w, 2); !p.Peek || p.Rect != (Rect{X: 95, Y: 8, W: 80, H: 64}) {
		t.Fatalf("right peek %+v", p)
	}
	w.Apply(ActionFocusColumnLeft)
	w.Apply(ActionFocusColumnLeft)
	w.Apply(ActionFocusWindowUp)
	if id, _ := w.Focused(); id != 4 {
		t.Fatalf("focused %d, want the first", id)
	}
	// Column actions do nothing while the stash has the focus.
	w.Apply(ActionMoveColumnLeft)
	w.Apply(ActionCycleColumnWidth)
	if w.Columns[0].Windows[0] != 1 || w.Columns[0].Width != (Width{}) {
		t.Fatalf("column changed: %+v", w.Columns)
	}
	// A gap as wide as the margin leaves nothing to show: neighbors wait
	// off screen.
	w.stashGap = 10
	if p := placement(w, 3); !p.Hidden || p.Peek {
		t.Fatalf("no peek: %+v", p)
	}
}

// Hiding the stash is the way out: tiles get the focus, a new stashed
// window shows it again, and the native floats stay as they are.
func TestToggleStashVisible(t *testing.T) {
	w := workspace()
	w.Apply(ActionToggleStashVisible)
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddWindow(3)
	w.Apply(ActionToggleWindowStash)
	w.FocusID(2)
	w.Apply(ActionToggleWindowStash)
	w.Apply(ActionFocusColumnLeft)
	w.Apply(ActionToggleStashVisible)
	if id, _ := w.Focused(); id != 1 || !w.stashHidden {
		t.Fatalf("focused %d hidden %v", id, w.stashHidden)
	}
	for _, id := range []WindowID{2, 3} {
		if p := placement(w, id); !p.Hidden || p.Focused {
			t.Fatalf("hidden %+v", p)
		}
	}
	if w.FocusID(2) {
		t.Fatal("focused hidden stash window")
	}
	// Shown from a tile: the stash takes the focus on its selected window.
	w.Apply(ActionToggleStashVisible)
	if id, _ := w.Focused(); id != 3 || w.stashHidden {
		t.Fatalf("restored focus %d", id)
	}
	w.Apply(ActionToggleStashVisible)
	w.FocusID(1)
	w.Apply(ActionToggleWindowStash)
	if id, _ := w.Focused(); id != 1 || w.stashHidden || w.stashAt != 2 {
		t.Fatalf("new stashed window: focused %d hidden %v", id, w.stashHidden)
	}
	// A native dialog stays when the stash hides.
	w.AddFloating(9, 10, 10)
	w.Apply(ActionToggleStashVisible)
	if p := placement(w, 9); p.Hidden || !w.stashHidden {
		t.Fatalf("dialog hidden with the stash: %+v", p)
	}
	if id, _ := w.Focused(); id != 9 {
		t.Fatalf("focused %d, want the dialog", id)
	}
}

// Closing the selected stashed window selects its right neighbor, else its
// left one, else gives the focus back to the tiles.
func TestStashClose(t *testing.T) {
	w := workspace()
	for id := WindowID(1); id <= 4; id++ {
		w.AddWindow(id)
	}
	for _, id := range []WindowID{4, 3, 2} {
		w.FocusID(id)
		w.Apply(ActionToggleWindowStash)
	}
	w.FocusID(3) // the middle one
	w.RemoveWindow(3)
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("after middle: %d", id)
	}
	w.RemoveWindow(2)
	if id, _ := w.Focused(); id != 4 {
		t.Fatalf("after last: %d", id)
	}
	w.RemoveWindow(4)
	if id, _ := w.Focused(); id != 1 || len(w.Stash) != 0 || w.stashFocus {
		t.Fatalf("after all: %d", id)
	}
}

// A fullscreen window is exclusive: a new window waits hidden without
// moving the view, in scroll and fixed overflow, and shows once it leaves.
func TestExclusiveFullscreenNewWindow(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		w := workspace()
		w.Overflow = overflow
		w.AddWindow(1)
		w.SetFullscreen(1, true)
		w.AddWindow(2)
		for _, p := range w.Layout() {
			if p.ID == 1 && (!p.Fullscreen || !p.Focused || p.Hidden) || p.ID == 2 && !p.Hidden {
				t.Fatalf("overflow %v: %+v", overflow, p)
			}
		}
		if w.ToggleStashVisible(); w.stashHidden {
			t.Fatal("floats toggled under fullscreen")
		}
		w.SetFullscreen(1, false)
		for _, p := range w.Layout() {
			if p.ID == 2 && p.Hidden {
				t.Fatalf("overflow %v: new window still hidden", overflow)
			}
		}
	}
}

// A window mapped under a covering fullscreen float with no column waits
// hidden as the first column.
func TestFullscreenFloatNewWindowNoColumns(t *testing.T) {
	w := workspace()
	w.AddFloating(1, 300, 200)
	w.SetFullscreen(1, true)
	w.AddWindow(2)
	if len(w.Columns) != 1 || w.Columns[0].Windows[0] != 2 {
		t.Fatalf("columns %+v", w.Columns)
	}
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("focus %d", id)
	}
}

// Under a covering fullscreen float, or any fixed-overflow fullscreen,
// focus moves inside the workspace are off and binds act on the window on
// screen, never on a column it hides.
func TestFullscreenPinsFocus(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddFloating(3, 20, 10)
	w.SetFullscreen(3, true)
	w.focusCover()
	w.Apply(ActionMoveColumnLeft)
	w.Apply(ActionCycleColumnWidth)
	if id, _ := w.Focused(); id != 3 || w.fullscreen != 3 || w.Columns[0].Windows[0] != 1 || w.Columns[1].Width != (Width{}) {
		t.Fatalf("focused %d fullscreen %d columns %+v", id, w.fullscreen, w.Columns)
	}
	if e := w.Apply(ActionCloseWindow); e.Close != 3 {
		t.Fatalf("close %d, want the fullscreen window", e.Close)
	}
}

// A focus move from a covering fullscreen window leaves fullscreen for the
// window on that side; with none there, fullscreen stays.
func TestFocusMoveLeavesFullscreen(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddFloating(3, 20, 10)
	w.SetFullscreen(3, true)
	w.FocusColumn(-1)
	if id, _ := w.Focused(); id == 3 || w.fullscreen != 0 {
		t.Fatalf("float: focused %d fullscreen %d", id, w.fullscreen)
	}
	f := workspace()
	f.Overflow = OverflowFixed
	f.AddWindow(1)
	f.AddWindow(2)
	f.SetFullscreen(2, true)
	if f.FocusColumn(1); f.fullscreen != 2 || f.Focus != 1 {
		t.Fatalf("fixed edge: focus %d fullscreen %d", f.Focus, f.fullscreen)
	}
	if f.FocusColumn(-1); f.fullscreen != 0 || f.Focus != 0 {
		t.Fatalf("fixed: focus %d fullscreen %d", f.Focus, f.fullscreen)
	}
	// Side by side: nothing above, so up keeps fullscreen and reports
	// the edge (the monitor goes to the workspace above).
	f.Focus = 1
	f.SetFullscreen(2, true)
	if f.FocusWindow(-1) || f.fullscreen != 2 || f.Focus != 1 {
		t.Fatalf("fixed up: focus %d fullscreen %d", f.Focus, f.fullscreen)
	}
	// Stacked: up reaches the window above in the column.
	f.SetFullscreen(2, false)
	f.stackWindow(2, 1)
	f.FocusID(2)
	f.SetFullscreen(2, true)
	if !f.FocusWindow(-1) || f.fullscreen != 0 {
		t.Fatalf("stacked up: fullscreen %d", f.fullscreen)
	}
	if id, _ := f.Focused(); id != 1 {
		t.Fatalf("stacked up: focused %d", id)
	}
	// Scroll overflow: moving off a fullscreen column still works; it no
	// longer covers.
	s := workspace()
	s.AddWindow(1)
	s.AddWindow(2)
	s.SetFullscreen(2, true)
	if s.FocusColumn(-1); s.Focus != 0 || s.cover() != 0 {
		t.Fatalf("scroll: focus %d cover %d", s.Focus, s.cover())
	}
}

// A fullscreen window does not float until it leaves fullscreen.
func TestToggleWindowStashIgnoresFullscreen(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.SetFullscreen(1, true)
	w.Apply(ActionToggleWindowStash)
	if len(w.Stash) != 0 || w.fullscreen != 1 {
		t.Fatalf("fullscreen window floated: %+v", w)
	}
	w.SetFullscreen(1, false)
	w.Apply(ActionToggleWindowStash)
	if len(w.Stash) != 1 {
		t.Fatal("window did not float after leaving fullscreen")
	}
}

// A lone maximized column comes back maximized.
func TestToggleWindowStashKeepsFullWidth(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddWindow(2)
	w.Apply(ActionMaximizeColumn)
	w.Apply(ActionToggleWindowStash)
	w.Apply(ActionToggleWindowStash)
	if id, _ := w.Focused(); id != 2 || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("focused %d, column %+v", id, w.Columns[w.Focus])
	}
}

// A lone expanded column (fixed overflow) comes back expanded.
func TestToggleWindowStashKeepsExpanded(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.SetMaxColumns(3)
	w.AddWindow(1)
	w.AddWindow(2)
	w.CycleWidth()
	if !w.Columns[w.Focus].Expanded {
		t.Fatal("column not expanded")
	}
	w.Apply(ActionToggleWindowStash)
	w.Apply(ActionToggleWindowStash)
	if id, _ := w.Focused(); id != 2 || !w.Columns[w.Focus].Expanded {
		t.Fatalf("focused %d, column %+v", id, w.Columns[w.Focus])
	}
}

// A client fullscreen request of a hidden stashed window leaves it hidden
// on its workspace, even in fixed overflow (which moves fullscreen windows
// out).
func TestHiddenFloatFullscreenRequest(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(100, 80)
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddWindow(2)
	w := m.Current()
	w.ToggleWindowStash()
	w.ToggleStashVisible()
	m.SetFullscreen(2, true)
	if m.Current() != w || w.stashIndex(2) < 0 || !w.stashHidden || w.fullscreen != 0 {
		t.Fatalf("hidden stash left or showed: %+v", w)
	}
	w.ToggleStashVisible()
	if w.fullscreen != 2 {
		t.Fatalf("fullscreen lost on show: %d", w.fullscreen)
	}
}

// A fullscreen stashed window (a game in scanout) keeps the stash shown:
// hiding it would drop scanout and VRR.
func TestToggleStashVisibleFullscreen(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.ToggleWindowStash()
	w.SetFullscreen(2, true)
	w.Apply(ActionToggleStashVisible)
	if id, _ := w.Focused(); id != 2 || w.fullscreen != 2 || w.stashHidden {
		t.Fatalf("fullscreen stash hidden: focused %d fullscreen %d", id, w.fullscreen)
	}
}

func TestFloatingSceneDimAndConfigure(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Floating.Dim = 0.3
	cfg.Border.Width = 2
	commands := make(chan ports.ClientCommand, 128)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	w := c.cur().mon.Current()
	c.cur().mon.SetOutput(100, 80)
	w.AddWindow(1)
	ctx := context.Background()
	check := func(want float64) {
		t.Helper()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		if s := (<-scenes)[0]; s.Dim != want {
			t.Fatalf("dim %v want %v", s.Dim, want)
		}
	}
	check(0)
	w.ToggleWindowStash()
	check(0.3)
	found := false
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 1 {
			if v.Floating {
				found = true
				if v.Width != 76 || v.Height != 60 {
					t.Fatalf("imposed configure %+v", v)
				}
			}
		}
	}
	if !found {
		t.Fatal("missing imposed configure")
	}
	w.AddFloating(2, 20, 10)
	check(0.3)
	found = false
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 2 {
			found = true
			if v.Width != 0 || v.Height != 0 {
				t.Fatalf("native configure %+v", v)
			}
		}
	}
	if !found {
		t.Fatal("missing native configure")
	}
	// Hiding the stash leaves the dialog, and its veil.
	w.ToggleStashVisible()
	check(0.3)
	w.RemoveWindow(2)
	check(0)
	w.ToggleStashVisible()
	check(0.3)
	w.AddFloating(2, 20, 10)
	w.SetFullscreen(1, true)
	check(0)
	w.SetFullscreen(1, false)
	w.ToggleWindowStash()
	w.SetFullscreen(1, true)
	check(0)
	// A dialog of a fullscreen tile waits hidden: no veil over the game.
	w.SetFullscreen(1, false)
	w.RemoveWindow(1)
	w.RemoveWindow(2)
	w.AddWindow(3)
	w.SetFullscreen(3, true)
	check(0)
	w.AddFloating(4, 20, 10)
	check(0)
	w.SetFullscreen(3, false)
	check(0.3)
}

// Activating a hidden stashed window shows the stash and focuses it; the
// fullscreen window it hid behind leaves fullscreen.
func TestActivateHiddenFloat(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.ToggleWindowStash()
	w.ToggleStashVisible()
	w.FocusID(1)
	w.SetFullscreen(1, true)
	w.Activate(2)
	if id, _ := w.Focused(); id != 2 || w.stashHidden || w.fullscreen != 0 {
		t.Fatalf("focused %d hidden %v fullscreen %d", id, w.stashHidden, w.fullscreen)
	}
	// The float's own fullscreen, asked while hidden, comes back with it.
	w.FocusID(2)
	w.ToggleStashVisible()
	w.SetFullscreen(2, true)
	w.SetFullscreen(1, true)
	w.Activate(2)
	if id, _ := w.Focused(); id != 2 || w.stashHidden || w.fullscreen != 2 {
		t.Fatalf("own fullscreen: focused %d hidden %v fullscreen %d", id, w.stashHidden, w.fullscreen)
	}
}
