package workspace

import (
	"slices"
	"testing"
)

func TestMarkArg(t *testing.T) {
	for _, tc := range []struct {
		action Action
		op     MarkOp
		name   string
		cmd    []string
		ok     bool
	}{
		{"mark a", MarkSet, "a", nil, true},
		{"toggle-mark a", MarkToggle, "a", nil, true},
		{"unmark a", MarkUnmark, "a", nil, true},
		{"unmark", MarkUnmark, "", nil, true},
		{"focus-mark a", MarkFocus, "a", nil, true},
		{"focus-mark a foot -e x", MarkFocus, "a", []string{"foot", "-e", "x"}, true},
		{"swap-mark a", MarkSwap, "a", nil, true},
		{"move-to-mark a", MarkMoveTo, "a", nil, true},
		{"mark", 0, "", nil, false},
		{"mark a b", 0, "", nil, false},
		{"mark a:b", 0, "", nil, false},
		{"swap-mark", 0, "", nil, false},
		{"unmark a b", 0, "", nil, false},
		{"marks a", 0, "", nil, false},
	} {
		op, name, cmd, ok := MarkArg(tc.action)
		if op != tc.op || name != tc.name || !slices.Equal(cmd, tc.cmd) || ok != tc.ok {
			t.Errorf("MarkArg(%q) = %v %q %q %v", tc.action, op, name, cmd, ok)
		}
	}
}
