package core

import (
	"reflect"
	"testing"
	"testing/synctest"
)

// A workspace created after the monitor setters carries those settings:
// it equals a fresh workspace given the same setters directly.
func TestNewWorkspaceTakesMonitorSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(200, 100)
		m.SetUsable(Rect{X: 10, Y: 5, W: 180, H: 90})
		m.SetGaps(7)
		m.SetBorder(2)
		m.SetStash(30, 4)
		m.SetMaxColumns(3)
		m.SetOverflow(OverflowFixed)
		m.AddWindow(1)
		spare := m.Workspaces[len(m.Workspaces)-1]
		if !spare.Empty() || spare == m.Current() {
			t.Fatalf("no spare workspace: %d", len(m.Workspaces))
		}

		want := &Workspace{}
		want.SetOutput(200, 100)
		want.SetUsable(Rect{X: 10, Y: 5, W: 180, H: 90})
		want.SetGaps(7)
		want.SetBorder(2)
		want.SetStash(30, 4)
		want.SetMaxColumns(3)
		want.SetOverflow(OverflowFixed)
		want.ID = spare.ID
		if !reflect.DeepEqual(spare, want) {
			t.Fatalf("spare %+v, want %+v", spare, want)
		}
		if spare.Output != (Rect{W: 200, H: 100}) || spare.Usable != (Rect{X: 10, Y: 5, W: 180, H: 90}) ||
			spare.Gaps != 7 || spare.MaxColumns != 3 || spare.Overflow != OverflowFixed {
			t.Fatalf("spare %+v", spare)
		}
	})
}

// A named workspace still gets its size and column count over the defaults.
func TestNamedWorkspaceKeepsSizeAndMaxColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(200, 100)
		m.SetMaxColumns(1)
		m.SetNamed([]NamedWorkspace{{Name: "dev", MaxColumns: 3, Size: [2]int{100, 50}}})
		m.Apply("workspace dev")
		w := m.Current()
		if w.Name != "dev" || w.MaxColumns != 3 || w.Output != (Rect{X: 50, Y: 25, W: 100, H: 50}) {
			t.Fatalf("%q max %d output %+v", w.Name, w.MaxColumns, w.Output)
		}
	})
}
