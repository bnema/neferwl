package app

import (
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/domain"
)

func TestLogInputTraceLevel(t *testing.T) {
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out}).WithField("component", "core")
	logInputTrace(log, domain.BindDispatched{Action: "focus-column-left", Source: domain.BindSourceKey, Window: 3})
	if out.String() != "" {
		t.Fatalf("focus bind logged at info: %s", out.String())
	}
	logInputTrace(log, domain.BindDispatched{Action: "close-window", Source: domain.BindSourceKey, Window: 3, Notable: true})
	for _, field := range []string{`"level":"info"`, `"component":"core"`, `"action":"close-window"`, `"window":3`, `"message":"bind dispatched"`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in %s", field, out.String())
		}
	}
}

func TestLogInputTraceWindowDropped(t *testing.T) {
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out}).WithField("component", "core")
	logInputTrace(log, domain.WindowDropped{Window: 4, Kind: "tab", Output: "A", OutputAfter: "B", WorkspaceBefore: 1, WorkspaceAfter: 2})
	for _, field := range []string{`"level":"info"`, `"component":"core"`, `"window":4`, `"kind":"tab"`,
		`"output":"A"`, `"output_after":"B"`, `"workspace_before":1`, `"workspace_after":2`, `"message":"window dropped"`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in %s", field, out.String())
		}
	}
}
