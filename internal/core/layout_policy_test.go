package core

import "testing"

func TestWorkspaceStepFollowsWorkspaceAxis(t *testing.T) {
	for _, tc := range []struct {
		name       string
		overflow   Overflow
		vertical   int
		horizontal int
	}{
		{"scroll", OverflowScroll, 1, 0},
		{"fixed", OverflowFixed, 1, 0},
		{"cascade", OverflowCascade, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Workspace{Overflow: tc.overflow}
			p := w.policy()
			for _, dir := range []int{-1, 1} {
				if got := p.workspaceStep(verticalAxis, dir); got != dir*tc.vertical {
					t.Errorf("vertical step %d = %d, want %d", dir, got, dir*tc.vertical)
				}
				if got := p.workspaceStep(horizontalAxis, dir); got != dir*tc.horizontal {
					t.Errorf("horizontal step %d = %d, want %d", dir, got, dir*tc.horizontal)
				}
			}
		})
	}
}
