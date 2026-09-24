package core

import (
	"reflect"
	"testing"
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
	if !m.FocusID(1) || m.Active != 0 {
		t.Fatal(m.Active)
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
	m.Apply(ActionMoveToWorkspaceDown)
	if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {}}) || m.Active != 0 {
		t.Fatal(windows(m), m.Active)
	}
	m.Apply("move-to-workspace 3")
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
	if p[0].Rect.W != 100 || !p[0].Borderless {
		t.Fatal(p)
	}
	m.AddWindow(2)
	m.AddWindow(3)
	p = m.Layout()
	if p[0].Rect.W != 33 || p[1].Rect.W != 33 || p[2].Rect.W != 33 || p[0].Borderless {
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

func TestWorkspaceArg(t *testing.T) {
	for _, tc := range []struct {
		a        Action
		n        int
		move, ok bool
	}{{"focus-workspace 3", 3, false, true}, {"move-to-workspace 12", 12, true, true}, {"focus-workspace 0", 0, false, false}, {"focus-workspace x", 0, false, false}, {"quit", 0, false, false}} {
		n, move, ok := WorkspaceArg(tc.a)
		if n != tc.n || move != tc.move || ok != tc.ok {
			t.Errorf("%s: %d %v %v", tc.a, n, move, ok)
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
	if p := m.Layout()[0]; p.Rect.W != 50 || p.Borderless {
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
	// Clicking a window of the hidden workspace shows it (FocusID).
	if !m.FocusID(2) || m.Current().Name != "dev" {
		t.Fatal("FocusID did not show the hidden workspace")
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
