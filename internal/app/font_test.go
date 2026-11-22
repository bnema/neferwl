package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"golang.org/x/image/font/gofont/goregular"
)

// An unreadable font file keeps the built-in font, with a config warning;
// the same path loads once the file is there.
func TestFontLoaderWarnsAndRetries(t *testing.T) {
	t.Cleanup(func() { _ = overlaydraw.LoadFont("") })
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out})
	fonts := fontLoader{log: log.WithField("component", "config")}
	path := filepath.Join(t.TempDir(), "missing.ttf")
	fonts.load(path)
	if overlaydraw.FontGen() != 0 {
		t.Fatal("not the built-in font")
	}
	for _, field := range []string{`"component":"config"`, `"level":"warn"`, `missing.ttf`, `using the built-in font`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in %s", field, out.String())
		}
	}
	if err := os.WriteFile(path, goregular.TTF, 0o600); err != nil {
		t.Fatal(err)
	}
	fonts.load(path)
	gen := overlaydraw.FontGen()
	if gen == 0 {
		t.Fatal("failed path not retried")
	}
	// A loaded path is not read again.
	fonts.load(path)
	if overlaydraw.FontGen() != gen {
		t.Fatal("loaded path reloaded")
	}
}
