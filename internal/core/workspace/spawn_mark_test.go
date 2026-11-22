package workspace

import (
	"slices"
	"testing"
)

func TestSpawnMarkFlag(t *testing.T) {
	for _, tc := range []struct {
		action Action
		mark   string
		cmd    []string
		ok     bool
	}{
		{"spawn --mark a foot", "a", []string{"foot"}, true},
		{"spawn  --mark  a  foot -e x", "a", []string{"foot", "-e", "x"}, true},
		{"spawn foot --mark a", "", []string{"foot", "--mark", "a"}, true},
		{"spawn foot", "", []string{"foot"}, true},
		{"spawn --mark", "", nil, false},
		{"spawn --mark a", "", nil, false},
		{"spawn --mark a:b foot", "", nil, false},
		{"spawn --marked foot", "", []string{"--marked", "foot"}, true},
	} {
		commands, mark, ok := SpawnCommands(tc.action)
		var cmd []string
		if len(commands) > 0 {
			cmd = commands[0]
		}
		if mark != tc.mark || ok != tc.ok || !slices.Equal(cmd, tc.cmd) {
			t.Errorf("SpawnCommands(%q) = %q %q %v", tc.action, commands, mark, ok)
		}
	}
	commands, mark, ok := StashCommands("stash --mark s firefox")
	if !ok || mark != "s" || len(commands) != 1 || commands[0][0] != "firefox" {
		t.Fatalf("StashCommands: %q %q %v", commands, mark, ok)
	}
	if e := (&Workspace{}).Apply("stash --mark s firefox"); !e.Stash || e.Mark != "s" {
		t.Fatalf("Apply: %+v", e)
	}
}
