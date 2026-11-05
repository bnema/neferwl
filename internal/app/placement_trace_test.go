package app

import (
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/domain"
)

func TestLogPlacementTrace(t *testing.T) {
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out})
	logPlacementTrace(log.WithField("component", "core"), domain.PlacementTrace{Window: 3, Path: "lineage", Output: "DP-1", Workspace: 7, Launcher: 1, Cover: 2, WorkspaceFocusChanged: true})
	for _, field := range []string{`"component":"core"`, `"message":"window placed"`, `"window":3`, `"path":"lineage"`, `"output":"DP-1"`, `"workspace":7`, `"launcher":1`, `"cover":2`, `"workspace_focus_changed":true`, `"seat_focus_changed":false`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in %s", field, out.String())
		}
	}
	if strings.Count(out.String(), `"message":"window placed"`) != 1 {
		t.Fatal(out.String())
	}
}
