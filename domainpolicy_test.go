package main_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The core domain packages form layers below the Core service: each one may
// import the standard library, the value types of internal/ports and the
// domain packages listed below it. Nothing else from this module, and no
// context: domain code owns no event loop.
var domainLayers = map[string][]string{
	"internal/domain":         {},
	"internal/core/geom":      {"internal/domain", "internal/ports"},
	"internal/core/motion":    {"internal/domain", "internal/ports", "internal/core/geom"},
	"internal/core/layout":    {"internal/domain", "internal/ports", "internal/core/geom"},
	"internal/core/workspace": {"internal/domain", "internal/ports", "internal/core/geom", "internal/core/motion", "internal/core/layout"},
}

// domainStd are the standard packages domain code may import: pure data
// structures, formatting of numbers and text, and the time value types. Any
// other standard package (I/O, logging, concurrency, reflection) fails.
var domainStd = []string{"cmp", "image", "math", "os", "regexp", "slices", "strconv", "strings", "time"}

// domainForbiddenTime are the time package functions that read a clock or
// wait. Domain code takes instants as arguments; the time.Time and
// time.Duration types stay allowed.
var domainForbiddenTime = []string{"Now", "Since", "Until", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Sleep"}

// domainMocks are the generated mock directories under the domain packages.
var domainMocks = []string{"internal/core/layout/layoutmocks"}

// domainFiles lists the non-test Go files of a domain package directory,
// whatever their build tags or GOOS suffix: go list skips the excluded ones,
// the directory listing does not.
func domainFiles(t *testing.T, rel string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.FromSlash(rel), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(files, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
}

// fileImports parses only the import block of one source file.
func fileImports(t *testing.T, name string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var imports []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("%s: import %s: %v", name, imp.Path.Value, err)
		}
		imports = append(imports, path)
	}
	return imports
}

// TestDomainPackagesImportOnlyLowerLayers checks production imports only,
// parsed from every non-test file so a file excluded by a build tag or GOOS
// suffix is checked too: tests of domain packages may import testify and the
// generated mocks.
func TestDomainPackagesImportOnlyLowerLayers(t *testing.T) {
	const module = "github.com/bnema/neferwl/"
	for rel, allowed := range domainLayers {
		files := domainFiles(t, rel)
		if len(files) == 0 {
			t.Errorf("%s: no production files checked", rel)
		}
		for _, name := range files {
			for _, imp := range fileImports(t, name) {
				switch {
				case strings.HasPrefix(imp, module):
					if dep := strings.TrimPrefix(imp, module); !slices.Contains(allowed, dep) {
						t.Errorf("%s imports %s; allowed: %v", name, dep, allowed)
					}
				case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
					t.Errorf("%s imports third-party %s; domain code uses the standard library only", name, imp)
				case !slices.Contains(domainStd, imp):
					t.Errorf("%s imports %s; allowed standard packages: %v", name, imp, domainStd)
				}
			}
		}
	}
}

// TestDomainPackageTreeIsListed fails for a production package nobody
// classified: every directory with Go files under a domain package is itself
// in domainLayers or is a known generated mock directory (domainMocks) whose
// files all carry a "// Code generated" header. Test files do not count.
func TestDomainPackageTreeIsListed(t *testing.T) {
	for rel := range domainLayers {
		err := filepath.WalkDir(filepath.FromSlash(rel), func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			dir := filepath.ToSlash(path)
			if _, listed := domainLayers[dir]; listed {
				return nil
			}
			goFiles, err := filepath.Glob(filepath.Join(path, "*.go"))
			if err != nil {
				return err
			}
			goFiles = slices.DeleteFunc(goFiles, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
			if len(goFiles) == 0 {
				return nil
			}
			if !slices.Contains(domainMocks, dir) {
				t.Errorf("%s holds Go files but is neither in domainLayers nor in domainMocks", dir)
				return nil
			}
			for _, name := range goFiles {
				src, err := os.ReadFile(name)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(string(src), "// Code generated") {
					t.Errorf("%s: file in a mocks directory lacks a \"// Code generated\" header", name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestDomainPackagesReadNoClock rejects clock reads, timers and sleeps in the
// non-test files of the domain packages, whatever the build tags. Test files
// may use them (inside synctest bubbles, per AGENTS.md).
func TestDomainPackagesReadNoClock(t *testing.T) {
	for rel := range domainLayers {
		files := domainFiles(t, rel)
		for _, name := range files {
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range clockUses(t, name, src) {
				t.Errorf("%s: %s; domain code takes instants as arguments and owns no clock or timer", name, bad)
			}
		}
		if len(files) == 0 {
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
