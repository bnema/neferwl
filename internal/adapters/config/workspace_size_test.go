package config

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestWorkspaceSize(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  [2]int
		valid bool
	}{
		{"1920x1080", [2]int{1920, 1080}, true},
		{"inherit", [2]int{}, true},
		{"auto", [2]int{}, false},
		{"0x1080", [2]int{}, false},
		{"1920x0", [2]int{}, false},
		{"1920x1080@60", [2]int{}, false},
		{"-1x1080", [2]int{}, false},
		{"1920", [2]int{}, false},
		{"999999999999999999999x1080", [2]int{}, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			var ws ports.WorkspaceConfig
			err := setWorkspace(&ws, "size", tc.value)
			if (err == nil) != tc.valid || ws.Size != tc.want {
				t.Fatalf("size %v, err %v; want %v valid=%v", ws.Size, err, tc.want, tc.valid)
			}
		})
	}
}

func TestWorkspaceSizeParserAndInheritance(t *testing.T) {
	cfg, warnings := parseString(t, "workspace.presentation.size = 1920x1080\nbind.Cmd+p = workspace presentation\n")
	if len(warnings) != 0 || len(cfg.Workspaces) != 1 || cfg.Workspaces[0].Size != [2]int{1920, 1080} {
		t.Fatalf("config %+v, warnings %v", cfg.Workspaces, warnings)
	}
	cfg, warnings = parseString(t, "workspace.presentation.size = 1920x1080\nworkspace.presentation.size = inherit\nbind.Cmd+p = workspace presentation\n")
	// Duplicate keys may warn; the final value must restore inheritance.
	if len(cfg.Workspaces) != 1 || cfg.Workspaces[0].Size != [2]int{} {
		t.Fatalf("inherit %+v, warnings %v", cfg.Workspaces, warnings)
	}
}
