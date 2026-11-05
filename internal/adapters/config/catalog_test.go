package config

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestCatalogBindActions(t *testing.T) {
	args := map[string]string{
		"focus-workspace":          "2",
		"move-column-to-workspace": "2",
		"move-window-to-workspace": "2",
		"workspace":                "dev",
		"spawn":                    "foot --app-id=terminal",
		"stash":                    "foot",
		"set-column-width":         "+1",
		"set-window-height":        "-10%",
	}
	for _, spec := range domain.Commands() {
		t.Run(spec.Name, func(t *testing.T) {
			text := spec.Name
			action := spec.Kind == domain.CommandAction || spec.Kind == domain.CommandActionArg
			if spec.Kind == domain.CommandActionArg {
				arg, ok := args[spec.Name]
				if !ok {
					t.Fatalf("add a valid argument fixture for %s", spec.Name)
				}
				text += " " + arg
				if err := checkAction(spec.Name); err == nil {
					t.Error("bare argument action accepted")
				}
			}
			err := checkAction(text)
			if action && err != nil {
				t.Errorf("action rejected: %v", err)
			}
			if !action && err == nil {
				t.Error("non-action accepted as bind")
			}
		})
	}
	for _, text := range []string{"state extra", "key Super+x", "type hello", "pointer 1 2", "swipe down", "shot DP-1", "output-add 1920x1080@60", "output-rm HEADLESS-2"} {
		if err := checkAction(text); err == nil {
			t.Errorf("non-action %q accepted", text)
		}
	}
}
