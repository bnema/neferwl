package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseDebug(t *testing.T) {
	for _, tc := range []struct {
		input     string
		wantError bool
		selected  []string
	}{
		{"", false, nil}, {"core,app", false, []string{"core", "app"}},
		{"all", false, []string{"all"}}, {"unknown", true, nil}, {"core,", true, nil},
	} {
		got, err := ParseDebug(tc.input)
		if (err != nil) != tc.wantError {
			t.Fatalf("ParseDebug(%q): %v", tc.input, err)
		}
		for _, name := range tc.selected {
			if !got[name] {
				t.Errorf("ParseDebug(%q) missing %s", tc.input, name)
			}
		}
	}
}

func TestComponentLevelsAndRotation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "nefertty", "nefertty.log")
	for run := 0; run < 2; run++ {
		ctx, closeLog, err := Open(context.Background(), "info", "core")
		if err != nil {
			t.Fatal(err)
		}
		core := For(ctx, "core")
		app := For(ctx, "app")
		core.Debug().Msg("visible")
		app.Debug().Msg("hidden")
		app.Info().Msg("startup")
		if err := closeLog(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := splitLines(data)
		if len(lines) != 2 {
			t.Fatalf("expected 2 entries, got %q", data)
		}
		for _, line := range lines {
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Fatal(err)
			}
			if entry["component"] != "core" && entry["component"] != "app" {
				t.Fatalf("missing component: %v", entry)
			}
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal(err)
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
