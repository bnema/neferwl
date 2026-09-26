package core

import (
	"reflect"
	"slices"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
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

func TestMonitorFirstWorkspaceStays(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.RemoveWindow(1)
	m.FocusNumber(2)
	if len(m.Workspaces) != 1 || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	// Leaving an empty first workspace does not remove it.
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.FocusNumber(1)
	m.RemoveWindow(1)
	m.FocusNumber(2)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{}, {2}, {}}) {
		t.Fatal(windows(m))
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

func TestMonitorFullscreenRequestStaysPut(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.FocusNumber(2)
	m.AddWindow(2)
	m.SetFullscreen(1, true)
	if m.Active != 1 {
		t.Fatal("client request switched workspace")
	}
	if m.Workspaces[0].fullscreen != 1 {
		t.Fatal("fullscreen not applied")
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
	m.Apply(ActionFocusWorkspaceUp)
	if m.Active != 0 {
		t.Fatal(m.Active)
	}
	// Move the focused window (2) down: focus stays on workspace 1.
	m.Apply(ActionMoveWindowToWorkspaceDown)
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
	// Workspace 1 is empty but first: it stays.
	if len(m.Workspaces) != 4 {
		t.Fatal(windows(m))
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
	if w := m.Current(); w.columnWidth(3) != 33 || w.ViewX == 0 {
		t.Fatal(w.ViewX)
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
		m.Apply(ActionMoveWindowToWorkspaceDown)
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {3}, {}}) {
			t.Fatal(got)
		}
	})
	t.Run("follow shows the target", func(t *testing.T) {
		m := stack()
		m.SetFollowMove(true)
		m.Apply(ActionMoveColumnToWorkspaceDown)
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
		m.Apply(ActionMoveWindowToWorkspaceDown)
		if c := m.Current().Columns[1]; c.Slot != 0 || c.Windows[0] != 3 {
			t.Fatal(c)
		}
	})
	t.Run("follow a floating window", func(t *testing.T) {
		m := stack()
		m.SetFollowMove(true)
		m.AddFloating(9, 10, 10)
		m.Current().FocusID(9)
		m.Apply(ActionMoveWindowToWorkspaceDown)
		if id, _ := m.Focused(); m.Active != 1 || id != 9 {
			t.Fatal(m.Active, id)
		}
	})
	t.Run("slot column loses its slot", func(t *testing.T) {
		m := stack()
		m.Current().Columns[1].Slot = 2
		m.Apply(ActionMoveColumnToWorkspaceDown)
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

func TestFullscreenRequestKeepsFocus(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	m.AddWindow(2)
	m.SetFullscreen(1, true)
	if id, _ := m.Focused(); id != 2 {
		t.Fatal(id)
	}
	if m.Current().fullscreen != 1 {
		t.Fatal("not fullscreen")
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

func TestHiddenWorkspaceToggle(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev", Hidden: true})
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
	m.Apply(ActionFocusWorkspaceDown)
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
	m.Apply(ActionMoveWindowToWorkspaceDown)
	m.Apply(ActionMoveWindowToWorkspaceUp)
	if id, _ := m.Focused(); id != 2 || m.Current().Name != "dev" {
		t.Fatal(id, windows(m))
	}
}

func TestNamedWorkspaceIsNumberedAndKept(t *testing.T) {
	m := monitor()
	m.AddWindow(1)
	named(m, NamedWorkspace{Name: "web"})
	// Inserted above the trailing empty workspace: [1:{1}] [2:web] [3:{}].
	if len(m.Workspaces) != 3 || m.Workspaces[1].Name != "web" || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	// Empty named workspaces are not removed when left.
	m.FocusNumber(2)
	m.FocusNumber(1)
	if m.Workspaces[1].Name != "web" {
		t.Fatal(windows(m))
	}
	m.Apply("workspace web")
	if m.Active != 1 {
		t.Fatal(m.Active)
	}
	m.Apply("workspace web")
	if m.Active != 0 {
		t.Fatal("toggle did not return", m.Active)
	}
	m.Apply("workspace nope") // unknown: nothing
	if m.Active != 0 {
		t.Fatal(m.Active)
	}
}

func TestNamedWorkspaceReload(t *testing.T) {
	m := named(monitor(), NamedWorkspace{Name: "dev", Hidden: true}, NamedWorkspace{Name: "web"})
	m.Apply("workspace dev")
	m.AddWindow(7)
	// Reload keeps windows and the workspace on screen; web becomes hidden.
	named(m, NamedWorkspace{Name: "dev", Hidden: true}, NamedWorkspace{Name: "web", Hidden: true})
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
	m := named(monitor(), NamedWorkspace{Name: "dev", Hidden: true, MaxColumns: 3, Overflow: OverflowFixed})
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

// checkInvariants fails when the monitor is in a state no action should reach.
func checkInvariants(t *testing.T, m *Monitor, want []WindowID) {
	t.Helper()
	if m.Active < 0 || m.Active >= len(m.Workspaces) {
		t.Fatalf("active %d of %d", m.Active, len(m.Workspaces))
	}
	if last := m.Workspaces[len(m.Workspaces)-1]; !last.empty() || last.Name != "" {
		t.Fatal("no trailing empty unnamed workspace")
	}
	if m.shown != nil && indexOf(m.hidden, m.shown) < 0 {
		t.Fatal("shown is not a hidden workspace")
	}
	if m.back != nil && !m.has(m.back) {
		t.Fatal("back dangles")
	}
	seen := map[WindowID]int{}
	for _, w := range append(append([]*Workspace(nil), m.Workspaces...), m.hidden...) {
		for _, c := range w.Columns {
			for _, id := range c.Windows {
				seen[id]++
			}
		}
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Fatalf("window %d present %d times", id, seen[id])
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("windows %v, want %v", seen, want)
	}
}

func TestSetNamedTransitions(t *testing.T) {
	dev := NamedWorkspace{Name: "dev"}
	devHidden := NamedWorkspace{Name: "dev", Hidden: true}
	web := NamedWorkspace{Name: "web"}
	for _, tc := range []struct {
		name   string
		before []NamedWorkspace
		after  []NamedWorkspace
		onDev  bool   // show dev before the reload
		want   string // workspace name on screen after
	}{
		{"numbered active becomes hidden", []NamedWorkspace{dev}, []NamedWorkspace{devHidden}, true, "dev"},
		{"hidden shown becomes numbered", []NamedWorkspace{devHidden}, []NamedWorkspace{dev}, true, "dev"},
		{"rename drops the old name", []NamedWorkspace{dev}, []NamedWorkspace{web}, true, ""},
		{"hidden shown is removed", []NamedWorkspace{devHidden}, nil, true, ""},
		{"hidden not shown is removed", []NamedWorkspace{devHidden, web}, []NamedWorkspace{web}, false, ""},
		{"add while on numbered", nil, []NamedWorkspace{devHidden, web}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := named(monitor(), tc.before...)
			m.AddWindow(1)
			if tc.onDev {
				m.Apply("workspace dev")
			}
			m.AddWindow(2)
			checkInvariants(t, m, []WindowID{1, 2})
			m.SetNamed(tc.after)
			checkInvariants(t, m, []WindowID{1, 2})
			if got := m.Current().Name; got != tc.want {
				t.Fatalf("on %q, want %q", got, tc.want)
			}
			if id, _ := m.Focused(); id != 2 {
				t.Fatalf("focus moved to %d", id)
			}
		})
	}
}

// Under fixed overflow, fullscreen moves the window to its own workspace
// below; workspace up/down still work and leaving brings it back in place.
func TestMonitorFixedFullscreenOwnWorkspace(t *testing.T) {
	m := monitor()
	m.SetOverflow(OverflowFixed)
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	m.Current().FocusID(2)
	m.ToggleFullscreen()
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 3}, {2}, {}}) || m.Active != 1 {
		t.Fatal(got, m.Active)
	}
	if p := m.Layout(); !slices.ContainsFunc(p, func(p Placement) bool { return p.ID == 2 && p.Fullscreen && !p.Hidden }) {
		t.Fatal(p)
	}
	m.Apply(ActionFocusWorkspaceUp)
	if m.Active != 0 {
		t.Fatal("stuck on the fullscreen workspace")
	}
	m.Apply(ActionFocusWorkspaceDown)
	m.ToggleFullscreen()
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3}, {}}) || m.Active != 0 {
		t.Fatal(got, m.Active)
	}
	if id, _ := m.Focused(); id != 2 {
		t.Fatal("focus", id)
	}

	// A client request moves it too, without taking the user along
	// unless the window is the focused one on screen.
	m.Current().FocusID(1)
	m.SetFullscreen(3, true)
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {3}, {}}) || m.Active != 0 {
		t.Fatal(got, m.Active)
	}
	m.SetFullscreen(3, false)
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3}, {}}) {
		t.Fatal(got)
	}

	// Closing the fullscreen window drops its workspace and returns.
	m.Current().FocusID(3)
	m.ToggleFullscreen()
	m.RemoveWindow(3)
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {}}) || m.Active != 0 {
		t.Fatal(got, m.Active)
	}

	// Scroll overflow keeps the niri behaviour: same workspace.
	m.SetOverflow(OverflowScroll)
	m.ToggleFullscreen()
	if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2}, {}}) {
		t.Fatal(got)
	}
}

// The fullscreen workspace link survives only while it holds just its
// window; exits restore the exact place and never move the user's focus.
func TestMonitorFixedFullscreenEdges(t *testing.T) {
	fixed := func() *Monitor {
		m := monitor()
		m.SetOverflow(OverflowFixed)
		for id := WindowID(1); id <= 3; id++ {
			m.AddWindow(id)
		}
		return m
	}
	t.Run("a window opened there floats, then tiles after it at home", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.AddWindow(4)
		if fs := m.Current(); fs.floatIndex(4) != 0 || fs.fullscreen != 2 {
			t.Fatal(fs.Floats, fs.fullscreen)
		}
		m.SetFullscreen(2, false)
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 4, 3}, {}}) || m.Active != 0 {
			t.Fatal(got, m.Active)
		}
	})
	t.Run("a joined window lands after its stack when columns shifted", func(t *testing.T) {
		m := fixed()
		w := m.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{5}}, {Windows: []WindowID{2, 3}}, {Windows: []WindowID{6}}}
		w.FocusID(3)
		m.ToggleFullscreen()
		m.AddWindow(7)
		m.RemoveWindow(1)
		m.ToggleFullscreen()
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{5, 2, 3, 7, 6}, {}}) {
			t.Fatal(got)
		}
	})
	t.Run("the bind from a dialog returns the fullscreen window", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.AddFloating(4, 10, 10) // focused dialog
		m.ToggleFullscreen()
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3}, {}}) || m.Current().floatIndex(4) != 0 {
			t.Fatal(got, m.Current().Floats)
		}
	})
	t.Run("a dialog asking fullscreen keeps the link", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.AddFloating(4, 10, 10)
		m.SetFullscreen(4, true)
		m.SetFullscreen(4, false)
		m.ToggleFullscreen()
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3}, {}}) {
			t.Fatal(got)
		}
	})
	t.Run("moved in by hand it floats", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.Apply(ActionFocusWorkspaceUp)
		m.Current().FocusID(1)
		m.Apply(ActionMoveWindowToWorkspaceDown)
		if fs := m.Workspaces[1]; fs.floatIndex(1) != 0 || fs.origin == nil {
			t.Fatal(fs.Floats)
		}
	})
	t.Run("a moved column floats window by window", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.Apply(ActionFocusWorkspaceUp)
		w := m.Current()
		ids := w.windows()
		w.Columns = []Column{{Windows: ids}}
		w.Focus = 0
		m.Apply(ActionMoveColumnToWorkspaceDown)
		fs := m.Workspaces[1]
		for _, id := range ids {
			if fs.floatIndex(id) < 0 {
				t.Fatal(id, fs.Floats)
			}
		}
		if fs.origin == nil || len(ids) < 2 {
			t.Fatal(fs.origin, ids)
		}
	})
	t.Run("closing it brings its dialogs home, below a focused float", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.AddFloating(4, 10, 10)
		m.Apply(ActionFocusWorkspaceUp)
		m.AddFloating(9, 10, 10)
		m.RemoveWindow(2)
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 3}, {}}) || m.Current().floatIndex(4) != 0 {
			t.Fatal(got, m.Current().Floats)
		}
		if id, _ := m.Focused(); id != 9 {
			t.Fatal("focus", id)
		}
	})
	t.Run("stack of three keeps its column", func(t *testing.T) {
		m := fixed()
		w := m.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3, 5}}}
		w.FocusID(3)
		m.ToggleFullscreen()
		m.RemoveWindow(2)
		m.ToggleFullscreen()
		if len(w.Columns) != 2 || !slices.Equal(w.Columns[1].Windows, []WindowID{3, 5}) {
			t.Fatal(w.Columns)
		}
	})
	t.Run("client exit keeps the user's focus", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(1)
		m.SetFullscreen(3, true)
		m.SetFullscreen(3, false)
		if id, _ := m.Focused(); id != 1 || m.Active != 0 {
			t.Fatal("focus moved to", id, m.Active)
		}
		m.Current().FocusID(3)
		m.SetFullscreen(1, true)
		m.SetFullscreen(1, false)
		if id, _ := m.Focused(); id != 3 {
			t.Fatal("focus moved to", id)
		}
	})
	t.Run("stacked slot window returns to its row", func(t *testing.T) {
		m := fixed()
		w := m.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3}, Slot: 2}}
		w.FocusID(3)
		m.ToggleFullscreen()
		m.ToggleFullscreen()
		if !reflect.DeepEqual(w.Columns[1], Column{Windows: []WindowID{2, 3}, Focus: 1, Slot: 2}) || w.Focus != 1 {
			t.Fatal(w.Columns, w.Focus)
		}
	})
	t.Run("moved out by hand unlinks it", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.Apply(ActionMoveWindowToWorkspaceUp)
		m.AddWindow(4)
		m.RemoveWindow(4)
		if m.Active != 1 {
			t.Fatal("view jumped to", m.Active)
		}
	})
	t.Run("client exit on screen keeps the window focused", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(1)
		m.ToggleFullscreen()
		m.SetFullscreen(1, false)
		if id, _ := m.Focused(); id != 1 || m.Active != 0 {
			t.Fatal("focus", id, m.Active)
		}
	})
	t.Run("stack gone: back as a column", func(t *testing.T) {
		m := fixed()
		w := m.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3}}, {Windows: []WindowID{4}}}
		w.FocusID(3)
		m.ToggleFullscreen()
		m.RemoveWindow(2)
		m.ToggleFullscreen()
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 3, 4}, {}}) || len(m.Current().Columns) != 3 {
			t.Fatal(got, m.Current().Columns)
		}
	})
	t.Run("floating window", func(t *testing.T) {
		m := fixed()
		m.AddFloating(4, 10, 10)
		m.ToggleFullscreen()
		if got := windows(m); !reflect.DeepEqual(got, [][]WindowID{{1, 2, 3}, {}, {}}) || m.Active != 1 || m.Workspaces[1].floatIndex(4) != 0 {
			t.Fatal(got, m.Active)
		}
		m.ToggleFullscreen()
		if w := m.Current(); m.Active != 0 || w.floatIndex(4) != 0 || !w.floatFocus {
			t.Fatal(m.Active, w.Floats)
		}
	})
	t.Run("origin gone unlinks it", func(t *testing.T) {
		m := fixed()
		m.Current().FocusID(2)
		m.ToggleFullscreen()
		m.take(m.Workspaces[0])
		m.ToggleFullscreen()
		if id, _ := m.Focused(); id != 2 || m.Current().fullscreen != 0 {
			t.Fatal(id)
		}
	})
}
