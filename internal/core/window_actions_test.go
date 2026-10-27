package core

import (
	"reflect"
	"testing"
	"testing/synctest"
)

func TestMoveWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.AddWindow(1)
		m.FocusNumber(2)
		m.AddWindow(2)
		m.FocusNumber(3)
		m.AddWindow(3)
		// [1:{1}] [2:{2}] [3:{3}] [4:{}]
		m.Apply(ActionMoveWorkspacePrev)
		if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {3}, {2}, {}}) || m.Active != 1 {
			t.Fatal(windows(m), m.Active)
		}
		m.Apply(ActionMoveWorkspacePrev)
		m.Apply(ActionMoveWorkspacePrev)
		if !reflect.DeepEqual(windows(m), [][]WindowID{{3}, {1}, {2}, {}}) || m.Active != 0 {
			t.Fatal(windows(m), m.Active)
		}
		m.Apply(ActionMoveWorkspaceNext)
		m.Apply(ActionMoveWorkspaceNext)
		// Never past the trailing empty workspace.
		m.Apply(ActionMoveWorkspaceNext)
		if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {3}, {}}) || m.Active != 2 {
			t.Fatal(windows(m), m.Active)
		}
		// The trailing empty workspace does not move.
		m.FocusNumber(4)
		m.Apply(ActionMoveWorkspacePrev)
		if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {3}, {}}) || m.Active != 3 {
			t.Fatal(windows(m), m.Active)
		}

	})
}

func TestMoveWorkspaceHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := named(monitor(), NamedWorkspace{Name: "dev"})
		m.AddWindow(1)
		m.FocusNumber(2)
		m.AddWindow(2)
		m.ToggleNamed("dev")
		m.Apply(ActionMoveWorkspacePrev)
		if !reflect.DeepEqual(windows(m), [][]WindowID{{1}, {2}, {}}) {
			t.Fatal(windows(m))
		}

	})
}
