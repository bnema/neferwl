package core

import (
	"context"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestToggleWindowFloating(t *testing.T) {
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
			w.Apply(ActionToggleWindowFloating)
			if len(w.Floats) != 1 || !w.Floats[0].back.holdsStack(Column{Windows: []WindowID{3}}) && stacked {
				t.Fatalf("float %+v", w.Floats)
			}
			p := w.Layout()[len(w.Layout())-1]
			if !p.Focused || p.Rect != (Rect{X: 10, Y: 8, W: 80, H: 64}) {
				t.Fatalf("placement %+v", p)
			}
			w.ResizeFloating(2, 12, 14)
			if w.Layout()[len(w.Layout())-1].Rect != p.Rect {
				t.Fatal("client resize moved imposed float")
			}
			w.Apply(ActionToggleWindowFloating)
			id, _ := w.Focused()
			if id != 2 || len(w.Floats) != 0 || w.Focus != 1 || w.Columns[1].Width != (Width{Pixels: 35}) || !slices.Contains(w.Columns[1].Windows, 2) {
				t.Fatalf("round trip: %+v", w)
			}
		})
	}
	w := workspace()
	w.AddFloating(4, 20, 10)
	w.Apply(ActionToggleWindowFloating)
	if len(w.Floats) != 0 || len(w.Columns) != 1 || w.Columns[0].Windows[0] != 4 {
		t.Fatalf("native float: %+v", w)
	}
}

func TestToggleFloatingVisible(t *testing.T) {
	w := workspace()
	w.Apply(ActionToggleFloatingVisible)
	w.AddWindow(1)
	w.AddFloating(2, 20, 10)
	w.AddFloating(3, 20, 10)
	w.Apply(ActionToggleFloatingVisible)
	if id, _ := w.Focused(); id != 1 {
		t.Fatalf("focused %d", id)
	}
	for _, p := range w.Layout()[1:] {
		if !p.Hidden || p.Focused {
			t.Fatalf("hidden %+v", p)
		}
	}
	if w.FocusID(2) {
		t.Fatal("focused hidden float")
	}
	w.Apply(ActionToggleFloatingVisible)
	if id, _ := w.Focused(); id != 3 {
		t.Fatalf("restored focus %d", id)
	}
	w.Apply(ActionToggleFloatingVisible)
	w.AddFloating(4, 12, 12)
	if w.floatsHidden || w.Layout()[3].Hidden {
		t.Fatal("new float did not reveal floats")
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
		if w.ToggleFloatingVisible(); w.floatsHidden {
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

// A fullscreen window does not float until it leaves fullscreen.
func TestToggleWindowFloatingIgnoresFullscreen(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.SetFullscreen(1, true)
	w.Apply(ActionToggleWindowFloating)
	if len(w.Floats) != 0 || w.fullscreen != 1 {
		t.Fatalf("fullscreen window floated: %+v", w)
	}
	w.SetFullscreen(1, false)
	w.Apply(ActionToggleWindowFloating)
	if len(w.Floats) != 1 {
		t.Fatal("window did not float after leaving fullscreen")
	}
}

// A lone maximized column comes back maximized.
func TestToggleWindowFloatingKeepsFullWidth(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddWindow(2)
	w.Apply(ActionMaximizeColumn)
	w.Apply(ActionToggleWindowFloating)
	w.Apply(ActionToggleWindowFloating)
	if id, _ := w.Focused(); id != 2 || !w.Columns[w.Focus].FullWidth {
		t.Fatalf("focused %d, column %+v", id, w.Columns[w.Focus])
	}
}

// A lone expanded column (fixed overflow) comes back expanded.
func TestToggleWindowFloatingKeepsExpanded(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.SetMaxColumns(3)
	w.AddWindow(1)
	w.AddWindow(2)
	w.CycleWidth()
	if !w.Columns[w.Focus].Expanded {
		t.Fatal("column not expanded")
	}
	w.Apply(ActionToggleWindowFloating)
	w.Apply(ActionToggleWindowFloating)
	if id, _ := w.Focused(); id != 2 || !w.Columns[w.Focus].Expanded {
		t.Fatalf("focused %d, column %+v", id, w.Columns[w.Focus])
	}
}

// A client fullscreen request of a hidden float leaves it hidden on its
// workspace, even in fixed overflow (which moves fullscreen windows out).
func TestHiddenFloatFullscreenRequest(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(100, 80)
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddFloating(2, 20, 10)
	w := m.Current()
	w.ToggleFloatingVisible()
	m.SetFullscreen(2, true)
	if m.Current() != w || w.floatIndex(2) < 0 || !w.floatsHidden || w.fullscreen != 0 {
		t.Fatalf("hidden float left or showed: %+v", w)
	}
	w.ToggleFloatingVisible()
	if w.fullscreen != 2 {
		t.Fatalf("fullscreen lost on show: %d", w.fullscreen)
	}
}

// Only a focused float hides the floats, and never over a fullscreen float.
func TestToggleFloatingVisibleFocusAndFullscreen(t *testing.T) {
	w := workspace()
	w.Overflow = OverflowFixed
	w.AddWindow(1)
	w.AddFloating(2, 20, 10)
	w.FocusID(1)
	w.Apply(ActionToggleFloatingVisible)
	if id, _ := w.Focused(); id != 1 || w.floatsHidden {
		t.Fatalf("column window hid floats: focused %d hidden %v", id, w.floatsHidden)
	}
	// Shown again from a column window, the top float takes the focus.
	w.FocusID(2)
	w.Apply(ActionToggleFloatingVisible)
	w.FocusID(1)
	w.Apply(ActionToggleFloatingVisible)
	if id, _ := w.Focused(); id != 2 || w.floatsHidden {
		t.Fatalf("show from column: focused %d hidden %v", id, w.floatsHidden)
	}
	// A fullscreen float (a game in scanout) stays shown: hiding it would
	// drop scanout and VRR.
	w.FocusID(2)
	w.SetFullscreen(2, true)
	w.Apply(ActionToggleFloatingVisible)
	if id, _ := w.Focused(); id != 2 || w.fullscreen != 2 || w.floatsHidden {
		t.Fatalf("fullscreen float hidden: focused %d fullscreen %d", id, w.fullscreen)
	}
	w.FocusID(1)
	w.Apply(ActionToggleFloatingVisible)
	if w.fullscreen != 2 || w.floatsHidden {
		t.Fatalf("fullscreen float hidden from column: fullscreen %d", w.fullscreen)
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
	w.ToggleWindowFloating()
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
	w.ToggleFloatingVisible()
	check(0)
	w.ToggleFloatingVisible()
	check(0.3)
	w.SetFullscreen(1, true)
	check(0)
	w.SetFullscreen(1, false)
	w.ToggleWindowFloating()
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
