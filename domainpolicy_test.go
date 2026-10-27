package main_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func policyFiles(t *testing.T, dir string, check func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != dir {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		check(path, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPurePackageImports(t *testing.T) {
	const base = "github.com/bnema/neferwl/internal/"
	for _, rule := range []struct {
		dir   string
		allow []string
	}{
		{"domain", nil},
		{"core/geom", []string{"domain"}},
		{"core/layout", []string{"domain", "core/geom"}},
		{"core/motion", []string{"domain"}},
		{"core/workspace", []string{"domain", "core/geom", "core/layout", "core/motion"}},
	} {
		t.Run(rule.dir, func(t *testing.T) {
			policyFiles(t, "internal/"+rule.dir, func(path string, f *ast.File) {
				for _, imp := range f.Imports {
					name, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						t.Fatal(err)
					}
					allowed := false
					for _, dep := range rule.allow {
						allowed = allowed || name == base+dep
					}
					if allowed {
						continue
					}
					pkg, err := build.Default.Import(name, "", build.FindOnly)
					if err != nil || !pkg.Goroot {
						t.Errorf("%s imports forbidden dependency %s", path, name)
					}
				}
			})
		})
	}
}

func TestRootCoreHasNoDirectSideEffects(t *testing.T) {
	policyFiles(t, "internal/core", func(path string, f *ast.File) {
		if filepath.Dir(path) != "internal/core" {
			return
		}
		timeName := ""
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "crypto/rand", "reflect":
				t.Errorf("%s imports forbidden dependency %s", path, name)
			case "time":
				timeName = "time"
				if imp.Name != nil {
					timeName = imp.Name.Name
				}
				if timeName == "." {
					t.Errorf("%s: dot import of time bypasses clock policy", path)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != timeName {
				return true
			}
			switch sel.Sel.Name {
			case "Now", "Since", "NewTimer", "After", "Sleep":
				t.Errorf("%s calls time.%s; use ports.Clock", path, sel.Sel.Name)
			}
			return true
		})
	})
}
func TestPortsExportedTypesAreSealed(t *testing.T) {
	types, sealed := map[string]string{}, map[string]bool{}
	policyFiles(t, "internal/ports", func(path string, f *ast.File) {
		for _, decl := range f.Decls {
			switch n := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range n.Specs {
					typ, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if typ.Assign.IsValid() {
						t.Errorf("%s: ports must not declare aliases: %s", path, typ.Name.Name)
					}
					if _, isInterface := typ.Type.(*ast.InterfaceType); typ.Name.IsExported() && !isInterface {
						types[typ.Name.Name] = path
					}
				}
			case *ast.FuncDecl:
				if n.Recv != nil && !n.Name.IsExported() {
					recv := n.Recv.List[0].Type
					for {
						switch r := recv.(type) {
						case *ast.StarExpr:
							recv = r.X
						case *ast.IndexExpr:
							recv = r.X
						case *ast.IndexListExpr:
							recv = r.X
						default:
							goto unwrapped
						}
					}
				unwrapped:
					if id, ok := recv.(*ast.Ident); ok {
						sealed[id.Name] = true
					}
				}
			}
		}
	})
	// Seals are matched across all files, regardless of build constraints.
	for name, path := range types {
		if !sealed[name] {
			t.Errorf("%s: exported type %s has no unexported seal method declared in ports", path, name)
		}
	}
}
