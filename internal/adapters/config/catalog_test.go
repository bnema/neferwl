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
		"mode":                     "resize",
		"spawn":                    "foot --app-id=terminal",
		"stash":                    "foot",
		"set-column-width":         "+1",
		"set-window-height":        "-10%",
		"mark":                     "term",
		"toggle-mark":              "term",
		"unmark":                   "term",
		"focus-mark":               "term foot",
		"swap-mark":                "term",
		"move-to-mark":             "term",
	}
	// A bare unmark clears the focused window's marks.
	bareOK := map[string]bool{"unmark": true}
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
				if err := checkOneAction(spec.Name); err == nil && !bareOK[spec.Name] {
					t.Error("bare argument action accepted")
				}
			}
			err := checkOneAction(text)
			if action && err != nil {
				t.Errorf("action rejected: %v", err)
			}
			if !action && err == nil {
				t.Error("non-action accepted as bind")
			}
		})
	}
	for _, text := range []string{"state extra", "key Super+x", "type hello", "pointer 1 2", "swipe down", "shot DP-1", "output-add 1920x1080@60", "output-rm HEADLESS-2"} {
		if err := checkOneAction(text); err == nil {
			t.Errorf("non-action %q accepted", text)
		}
	}
}
