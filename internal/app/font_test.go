package app

import (
	"path/filepath"
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
)

// An unreadable font file keeps the built-in font, with a config warning.
func TestLoadFontWarns(t *testing.T) {
	t.Cleanup(func() { _ = overlaydraw.LoadFont("") })
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out})
	gen := overlaydraw.FontGen()
	loadFont(filepath.Join(t.TempDir(), "missing.ttf"), log.WithField("component", "config"))
	if overlaydraw.FontGen() == gen {
		t.Fatal("font not swapped back to the built-in one")
	}
	for _, field := range []string{`"component":"config"`, `"level":"warn"`, `missing.ttf`, `using the built-in font`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in %s", field, out.String())
		}
	}
}
