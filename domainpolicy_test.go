package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
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
var domainForbiddenStd = []string{"context", "log", "log/slog", "os", "os/exec", "net", "sync", "runtime", "unsafe", "syscall"}

// domainForbiddenTime are the time package functions that read a clock or
// wait. Domain code takes instants as arguments; the time.Time and
// time.Duration types stay allowed.
var domainForbiddenTime = []string{"Now", "Since", "Until", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Sleep"}

// domainPackage is the part of `go list -json` the layer check reads.
type domainPackage struct {
	ImportPath string
	Imports    []string
}

// TestDomainPackagesImportOnlyLowerLayers checks production imports only:
// `go list` Imports excludes test files, so tests of domain packages may
// import testify and the generated mocks.
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

// TestDomainPackagesReadNoClock rejects clock reads, timers and sleeps in the
// non-test files of the domain packages, whatever the build tags. Test files
// may use them (inside synctest bubbles, per AGENTS.md).
func TestDomainPackagesReadNoClock(t *testing.T) {
	for rel := range domainLayers {
		files, err := filepath.Glob(filepath.Join(filepath.FromSlash(rel), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			checked++
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range clockUses(t, name, src) {
				t.Errorf("%s: %s; domain code takes instants as arguments and owns no clock or timer", name, bad)
			}
		}
		if checked == 0 {
			t.Errorf("%s: no production files checked", rel)
		}
	}
}

// clockUses lists the forbidden time selectors in one source file.
func clockUses(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var found []string
	local := ""
	for _, imp := range f.Imports {
		if imp.Path.Value != `"time"` {
			continue
		}
		switch {
		case imp.Name == nil:
			local = "time"
		case imp.Name.Name == ".":
			found = append(found, fset.Position(imp.Pos()).String()+": dot import of time hides clock calls")
		case imp.Name.Name != "_":
			local = imp.Name.Name
		}
	}
	if local == "" {
		return found
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == local && slices.Contains(domainForbiddenTime, sel.Sel.Name) {
			found = append(found, fset.Position(sel.Pos()).String()+": time."+sel.Sel.Name)
		}
		return true
	})
	return found
}
