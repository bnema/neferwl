package core

import (
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func monitor() *Monitor {
	m := NewMonitor()
	m.SetOutput(100, 80)
	m.SetMaxColumns(2)
	return m
}

// windows lists each workspace's windows, in column order.
func windows(m *Monitor) [][]WindowID {
	var out [][]WindowID
	for _, w := range m.Workspaces {
		list := []WindowID{}
		for _, c := range w.Columns {
			list = append(list, c.Windows...)
		}
		out = append(out, list)
	}
	return out
}

func TestMonitorKeepsOneEmptyWorkspaceBelow(t *testing.T) {
	m := monitor()
	if len(m.Workspaces) != 1 || m.Active != 0 {
		t.Fatal(windows(m))
	}
	m.AddWindow(1)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {}}) {
		t.Fatal(windows(m))
	}
	// Cmd+9 past the end lands on the trailing empty workspace.
	m.FocusNumber(9)
	if m.Active != 1 {
		t.Fatal(m.Active)
	}
	m.AddWindow(2)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {}}) {
		t.Fatal(windows(m))
	}
}

func TestMonitorRemovesEmptyWorkspaceOnceLeft(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(3)
	m.AddWindow(3)
	// [1:{1}] [2:{2}] [3:{3}] [4:{}]
	m.FocusNumber(2)
	m.RemoveWindow(2)
	// Still on the now-empty workspace 2: it stays while active.
	if len(m.Workspaces) != 4 || m.Active != 1 {
		t.Fatal(windows(m), m.Active)
	}
	m.FocusNumber(1)
	// Once left it is removed: workspace 3 becomes 2.
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {3}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.FocusNumber(2)
	if id, _ := m.Focused(); id != 3 {
		t.Fatal(id)
	}
}

func TestMonitorEmptyFirstWorkspaceRemovedOnceLeft(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.RemoveWindow(1)
	m.FocusNumber(2)
	// The only workspace stays.
	if len(m.Workspaces) != 1 || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(1)
	m.RemoveWindow(1)
	// Still on the now-empty workspace 1: it stays while active.
	if !reflect.DeepEqual(windows(m), [][]WindowID{{}, {2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	// Once left it is removed: workspace 2 becomes 1.
	m.FocusNumber(2)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
}

// A window closing elsewhere empties workspace 1: the first non-empty
// workspace is promoted at once and the view stays on its windows.
func TestMonitorEmptyFirstWorkspaceRemovedFromElsewhere(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.RemoveWindow(1)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
}

func TestMonitorRemoveElsewhereKeepsActive(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(3)
	m.AddWindow(3)
	m.FocusNumber(3)
	// A window on another workspace closes: the user stays on workspace 3's windows.
	m.RemoveWindow(2)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {3}, {}}) || m.Active != 1 {
		t.Fatal(windows(m), m.Active)
	}
	if id, _ := m.Focused(); id != 3 {
		t.Fatal(id)
	}
}

func TestMonitorLayoutHidesOtherWorkspaces(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	for _, p := range m.Layout() {
		if hidden := p.ID == 1; p.Hidden != hidden {
			t.Fatal(m.Layout())
		}
	}
}

func TestMonitorActions(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.AddWindow(2)
	// Down past the only window of the column goes to the workspace below.
	m.Apply(ActionFocusWindowDown)
	if m.Active != 1 {
		t.Fatal(m.Active)
	}
	m.Apply(ActionFocusWindowUp)
	if m.Active != 0 {
		t.Fatal(m.Active)
	}
	// Up at the top workspace stays.
	m.Apply(ActionFocusWorkspacePrev)
	if m.Active != 0 {
		t.Fatal(m.Active)
	}
	// Move the focused window (2) down: focus stays on workspace 1.
	m.Apply(ActionMoveWindowToWorkspaceNext)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.Apply("move-window-to-workspace 3")
	if !reflect.DeepEqual(windows(m), [][]WindowID{{}, {2}, {1}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.Apply("focus-workspace 3")
	if id, _ := m.Focused(); id != 1 {
		t.Fatal(id)
	}
	// The empty workspace 1 is removed once left.
	if !reflect.DeepEqual(windows(m), [][]WindowID{{2}, {1}, {}}) || m.Active != 1 {
		t.Fatal(windows(m), m.Active)
	}
	if e := m.Apply("spawn foot"); !e.Spawn {
		t.Fatal(e)
	}
}

func TestEqualSharesAndBorderless(t *testing.T) {
	m := monitor()
	m.SetMaxColumns(3)
	m.AddWindow(1)
	p := m.Layout()
	if p[0].Rect.W != 100 || p[0].Neighbors != 0 {
		t.Fatal(p)
	}
	m.AddWindow(2)
	m.AddWindow(3)
	p = m.Layout()
	if p[0].Rect.W != 33 || p[1].Rect.W != 33 || p[2].Rect.W != 33 || p[0].Neighbors != ports.SideRight || p[0].Inset != ports.SideRight {
		t.Fatal(p)
	}
	// A fourth column scrolls: still 1/3 each.
	m.AddWindow(4)
	if w := m.Current(); w.columnWidth(3) != 33 || w.View == 0 {
		t.Fatal(w.View)
	}
	// A preset on one column is kept; auto columns keep their share.
	m.SetPresets([]Width{{Num: 1, Den: 2}})
	m.Apply(ActionCycleColumnWidth)
	if w := m.Current(); w.columnWidth(w.Focus) != 50 || w.columnWidth(0) != 33 {
		t.Fatal(w.columnWidth(w.Focus), w.columnWidth(0))
	}
}

// stack returns a monitor with columns [1] [2 3], focus on window 3.
func stack() *Monitor {
	m := monitor()
	m.AddWindow(1)
	m.AddWindow(2)
	m.AddWindow(3)
	w := m.Current()
	w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3}, Focus: 1}}
	w.Focus = 1
	return m
}

func TestMoveColumnToWorkspace(t *testing.T) {
	t.Run("whole column, focus stays", func(t *testing.T) {
		m := stack()
		m.Apply("move-column-to-workspace 2")
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1}, {2, 3}, {}}) || m.Active != 0 {
			t.Fatal(got, m.Active)
		}
		if w := m.Workspaces[1]; len(w.Columns) != 1 || w.Columns[0].Focus != 1 {
			t.Fatal(w.Columns)
		}
		if id, _ := m.Focused(); id != 1 {
			t.Fatal(id)
		}
	})
	t.Run("window only", func(t *testing.T) {
		m := stack()
		m.Apply(ActionMoveWindowToWorkspaceNext)
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {3}, {}}) {
			t.Fatal(got)
		}
	})
	t.Run("follow shows the target", func(t *testing.T) {
		m := stack()
		m.SetFollowMove(true)
		m.Apply(ActionMoveColumnToWorkspaceNext)
		if id, _ := m.Focused(); m.Active != 1 || id != 3 {
			t.Fatal(m.Active, id)
		}
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1}, {2, 3}, {}}) {
			t.Fatal(got)
		}
	})
	t.Run("the slot window alone releases its slot", func(t *testing.T) {
		m := stack()
		c := &m.Current().Columns[1]
		c.Slot, c.Focus = 2, 0
		m.Apply(ActionMoveWindowToWorkspaceNext)
		if c := m.Current().Columns[1]; c.Slot != 0 || c.Windows[0] != 3 {
			t.Fatal(c)
		}
	})
	t.Run("follow a floating window", func(t *testing.T) {
		m := stack()
		m.SetFollowMove(true)
		m.AddFloating(9, 10, 10)
		m.Current().FocusID(9)
		m.Apply(ActionMoveWindowToWorkspaceNext)
		if id, _ := m.Focused(); m.Active != 1 || id != 9 {
			t.Fatal(m.Active, id)
		}
	})
	t.Run("slot column loses its slot", func(t *testing.T) {
		m := stack()
		m.Current().Columns[1].Slot = 2
		m.Apply(ActionMoveColumnToWorkspaceNext)
		if c := m.Workspaces[1].Columns[0]; c.Slot != 0 {
			t.Fatal(c)
		}
	})
}

func TestWorkspaceArg(t *testing.T) {
	for _, tc := range []struct {
		a  Action
		n  int
		op WorkspaceOp
		ok bool
	}{{"focus-workspace 3", 3, FocusWorkspace, true}, {"move-column-to-workspace 12", 12, MoveColumnToWorkspace, true}, {"move-window-to-workspace 2", 2, MoveWindowToWorkspace, true}, {"move-to-workspace 2", 0, 0, false}, {"focus-workspace 0", 0, 0, false}, {"focus-workspace x", 0, 0, false}, {"quit", 0, 0, false}} {
		n, op, ok := WorkspaceArg(tc.a)
		if n != tc.n || op != tc.op || ok != tc.ok {
			t.Errorf("%s: %d %v %v", tc.a, n, op, ok)
		}
	}
}

func TestLonePresetColumnKeepsWidth(t *testing.T) {
	m := monitor()
	m.SetPresets([]Width{{Num: 1, Den: 2}})
	m.AddWindow(1)
	m.Apply(ActionCycleColumnWidth)
	if p := m.Layout()[0]; p.Rect.W != 50 || p.Neighbors != 0 {
		t.Fatal(p)
	}
}

func named(m *Monitor, specs ...NamedWorkspace) *Monitor {
	m.SetNamed(specs)
	return m
}

// An empty workspace 1 stays while a named workspace is shown over it, so
// the toggle can return to it; leaving it for another number drops it.
func TestEmptyFirstWorkspaceUnderNamed(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev"})
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(1)
	m.RemoveWindow(1)
	m.Apply("workspace dev")
	if !reflect.DeepEqual(windows(m), [][]WindowID{{}, {2}, {}}) || m.Current().Name != "dev" {
		t.Fatal(windows(m), m.Current().Name)
	}
	m.Apply("workspace dev")
	if m.Current() != m.Workspaces[0] || !reflect.DeepEqual(windows(m), [][]WindowID{{}, {2}, {}}) {
		t.Fatal(windows(m), m.Active)
	}
	m.FocusNumber(2)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
}

func TestHiddenWorkspaceToggle(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev"})
	m.AddWindow(1)
	if len(m.Workspaces) != 2 {
		t.Fatal("hidden workspace is numbered", windows(m))
	}
	m.Apply("workspace dev")
	if m.Current().Name != "dev" {
		t.Fatal(m.Current().Name)
	}
	m.AddWindow(2)
	// Up/down do not leave a hidden workspace.
	m.Apply(ActionFocusWorkspaceNext)
	m.Apply(ActionFocusWindowDown)
	if m.Current().Name != "dev" {
		t.Fatal("left the hidden workspace")
	}
	for _, p := range m.Layout() {
		if hidden := p.ID == 1; p.Hidden != hidden {
			t.Fatal(m.Layout())
		}
	}
	// The same bind returns to the previous workspace.
	m.Apply("workspace dev")
	if id, _ := m.Focused(); id != 1 || m.Current().Name != "" {
		t.Fatal(id, m.Current().Name)
	}
	// Cmd+N from the hidden workspace goes to that number.
	m.Apply("workspace dev")
	m.FocusNumber(1)
	if id, _ := m.Focused(); id != 1 {
		t.Fatal(id)
	}
	// Moving a window up/down from a hidden workspace does nothing.
	m.Apply("workspace dev")
	m.Apply(ActionMoveWindowToWorkspaceNext)
	m.Apply(ActionMoveWindowToWorkspacePrev)
	if id, _ := m.Focused(); id != 2 || m.Current().Name != "dev" {
		t.Fatal(id, windows(m))
	}
}

func TestNamedWorkspaceBindOnly(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "web"})
	if len(m.Workspaces) != 1 {
		t.Fatal(windows(m))
	}
	m.Apply("workspace web")
	if m.Current().Name != "web" {
		t.Fatal(m.Current().Name)
	}
	m.Apply("workspace web")
	if m.Current().Name != "" {
		t.Fatal(m.Current().Name)
	}
}

func TestNamedWorkspaceReload(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev"}, NamedWorkspace{Name: "web"})
	m.Apply("workspace dev")
	m.AddWindow(7)
	// Reload keeps windows and the workspace on screen; web becomes hidden.
	named(m, NamedWorkspace{Name: "dev"}, NamedWorkspace{Name: "web"})
	if m.Current().Name != "dev" || len(m.Workspaces) != 1 {
		t.Fatal(m.Current().Name, windows(m))
	}
	// Dropping dev from config hands its windows to the numbered workspace.
	named(m)
	if m.Current().Name != "" || !reflect.DeepEqual(windows(m), [][]WindowID{{7}, {}}) {
		t.Fatal(m.Current().Name, windows(m))
	}
}

func TestNamedWorkspaceSettings(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev", MaxColumns: 3, Overflow: OverflowFixed})
	m.SetMaxColumns(1)
	if m.Current().MaxColumns != 1 || m.Current().Overflow != "" {
		t.Fatal(m.Current().MaxColumns, m.Current().Overflow)
	}
	m.Apply("workspace dev")
	if m.Current().MaxColumns != 3 || m.Current().Overflow != OverflowFixed {
		t.Fatal(m.Current().MaxColumns, m.Current().Overflow)
	}
	// New dynamic workspaces follow the defaults.
	m.SetOverflow(OverflowScroll)
	m.Apply("workspace dev")
	m.FocusNumber(9)
	if m.Current().MaxColumns != 1 || m.Current().Overflow != OverflowScroll {
		t.Fatal(m.Current().MaxColumns, m.Current().Overflow)
	}
}

// Cmd+F on a window that made itself fullscreen returns it to its column
// (Wine opens monitor-sized apps fullscreen); the next press maximizes.
func TestMaximizeLeavesClientFullscreen(t *testing.T) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			m := monitor()
			m.SetOverflow(o)
			m.AddWindow(1)
			m.AddWindow(2)
			m.SetFullscreen(2, true)
			m.Apply(ActionMaximizeColumn)
			if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {}}) || m.Active != 0 {
				t.Fatal(got, m.Active)
			}
			w := m.Current()
			if w.fullscreen != 0 || w.Columns[1].FullWidth {
				t.Fatal(w.fullscreen, w.Columns)
			}
			if id, _ := m.Focused(); id != 2 {
				t.Fatal("focus", id)
			}
			m.Apply(ActionMaximizeColumn)
			if !w.Columns[1].FullWidth {
				t.Fatal(w.Columns)
			}
		})
	}
}

// Ranging over every workspace allocates nothing: normalize and the
// per-frame paths use it.
func TestMonitorAllAllocations(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.SetNamed([]NamedWorkspace{{Name: "s"}})
	n := 0
	if a := testing.AllocsPerRun(100, func() {
		for range m.all() {
			n++
		}
		m.byName("s")
	}); a != 0 {
		t.Fatalf("%v allocs", a)
	}
}

// Fixed overflow keeps a fullscreen window in place: no workspace is
// added, a window opening meanwhile waits hidden, and the bind or a client
// exit restores the tiles with the user's focus where it was.
func TestFixedFullscreenStaysInPlace(t *testing.T) {
	m := monitor()
	m.SetOverflow(OverflowFixed)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	w.FocusID(2)
	m.Apply(ActionCycleColumnWidth) // expanded: kept through fullscreen
	m.ToggleFullscreen()
	m.AddWindow(4)
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3, 4}, {}}) || m.Current() != w || w.cover() != 2 {
		t.Fatal(got, w.cover())
	}
	if id, _ := m.Focused(); id != 2 {
		t.Fatal("focus", id)
	}
	m.ToggleFullscreen()
	if id, _ := m.Focused(); id != 2 || w.fullscreen != 0 || !w.Columns[1].Expanded {
		t.Fatal("after bind", id, w.Columns)
	}
}

// A client fullscreen request never moves the view or the focus (ADR
// 011). In fixed overflow, where it would cover the screen and take the
// keyboard, only the focused window's request applies.
func TestFullscreenRequestStaysPut(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow Overflow
		id       WindowID // requests fullscreen; 1 is focused
		applied  bool
	}{
		{"scroll focused", OverflowScroll, 1, true},
		{"scroll unfocused", OverflowScroll, 2, true},
		{"scroll other workspace", OverflowScroll, 3, true},
		{"fixed focused", OverflowFixed, 1, true},
		{"fixed unfocused", OverflowFixed, 2, false},
		{"fixed other workspace", OverflowFixed, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := monitor()
			m.SetOverflow(tc.overflow)
			m.AddWindow(3) // alone, so focused on workspace 1
			m.FocusNumber(2)
			m.AddWindow(2)
			m.AddWindow(1)
			w, _ := m.find(tc.id)
			m.SetFullscreen(tc.id, true)
			if id, _ := m.Focused(); m.Active != 1 || id != 1 || (w.fullscreen == tc.id) != tc.applied {
				t.Fatal("active", m.Active, "focused", id, "fullscreen", w.fullscreen)
			}
			m.SetFullscreen(tc.id, false)
			if id, _ := m.Focused(); id != 1 || w.fullscreen != 0 || len(m.Workspaces) != 3 {
				t.Fatal("round trip", id, windows(m))
			}
		})
	}
}
