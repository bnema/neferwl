package app

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
)

// The config adapter keeps its own list of overflow values (adapters do not
// import core): every layout core resolves must parse there without a
// warning, and a value no layout has still produces exactly one.
func TestConfigAcceptsEveryCoreLayout(t *testing.T) {
	names := core.LayoutNames()
	if len(names) == 0 {
		t.Fatal("core lists no layout")
	}
	for _, name := range names {
		for _, key := range []string{"layout.overflow", "layout.DP-1.overflow"} {
			_, warnings, err := config.Parse(strings.NewReader(key + " = " + name + "\n"))
			if err != nil || len(warnings) != 0 {
				t.Errorf("%s = %s: warnings %v, err %v", key, name, warnings, err)
			}
		}
	}
	_, warnings, err := config.Parse(strings.NewReader("layout.overflow = no-such-layout\n"))
	if err != nil || len(warnings) != 1 {
		t.Fatalf("unknown layout: warnings %v, err %v", warnings, err)
	}
}
