package workspace

import (
	"testing"
	"testing/synctest"
)

func TestWorkspaceArg(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

	})
}
