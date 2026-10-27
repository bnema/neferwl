package main_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// policyFiles includes production files regardless of build tags and GOOS.
func policyFiles(t *testing.T, dir string, check func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
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
func TestDomainImportsOnlyStandardLibrary(t *testing.T) {
	policyFiles(t, "internal/domain", func(path string, f *ast.File) {
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, ".") {
				t.Errorf("%s imports %s; domain imports only the standard library", path, name)
			}
		}
	})
}
func TestPortsExportedTypesAreSealed(t *testing.T) {
	types, sealed := map[string]string{}, map[string]bool{}
	policyFiles(t, "internal/ports", func(path string, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				if _, isInterface := n.Type.(*ast.InterfaceType); n.Name.IsExported() && !isInterface {
					types[n.Name.Name] = path
				}
			case *ast.FuncDecl:
				if n.Recv != nil && !n.Name.IsExported() {
					recv := n.Recv.List[0].Type
					if ptr, ok := recv.(*ast.StarExpr); ok {
						recv = ptr.X
					}
					if id, ok := recv.(*ast.Ident); ok {
						sealed[id.Name] = true
					}
				}
			}
			return true
		})
	})
	for name, path := range types {
		if !sealed[name] {
			t.Errorf("%s: exported type %s has no unexported seal method declared in ports", path, name)
		}
	}
}
