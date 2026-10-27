package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The core domain packages form layers below the Core service: each one may
// import the standard library, the value types of internal/ports and the
// domain packages listed below it. Nothing else from this module, and no
// context: domain code owns no event loop.
var domainLayers = map[string][]string{
	"internal/core/geom":      {"internal/ports"},
	"internal/core/motion":    {"internal/ports", "internal/core/geom"},
	"internal/core/layout":    {"internal/ports", "internal/core/geom"},
	"internal/core/workspace": {"internal/ports", "internal/core/geom", "internal/core/motion", "internal/core/layout"},
}

// domainForbiddenStd are standard packages domain code must not import.
var domainForbiddenStd = []string{"context", "log", "log/slog", "os", "os/exec", "net", "sync"}

// domainPackage is the part of `go list -json` the layer check reads.
type domainPackage struct {
	ImportPath string
	Imports    []string
}

func TestDomainPackagesImportOnlyLowerLayers(t *testing.T) {
	const module = "github.com/bnema/neferwl/"
	args := []string{"list", "-json=ImportPath,Imports"}
	for p := range domainLayers {
		args = append(args, "./"+p)
	}
	cmd := exec.Command("go", args...)
	cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	seen := 0
	dec := json.NewDecoder(bufio.NewReader(bytes.NewReader(out)))
	for dec.More() {
		var p domainPackage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		rel := strings.TrimPrefix(p.ImportPath, module)
		allowed, ok := domainLayers[rel]
		if !ok {
			t.Fatalf("unexpected package %s", p.ImportPath)
		}
		seen++
		for _, imp := range p.Imports {
			switch {
			case strings.HasPrefix(imp, module):
				if dep := strings.TrimPrefix(imp, module); !slices.Contains(allowed, dep) {
					t.Errorf("%s imports %s; allowed: %v", rel, dep, allowed)
				}
			case slices.Contains(domainForbiddenStd, imp):
				t.Errorf("%s imports %s; domain code owns no I/O, logging or concurrency", rel, imp)
			case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
				t.Errorf("%s imports third-party %s; domain code uses the standard library only", rel, imp)
			}
		}
	}
	if seen != len(domainLayers) {
		t.Fatalf("checked %d domain packages, want %d", seen, len(domainLayers))
	}
}
