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
		if entry.IsDir() && dir == "internal/ports" && path != dir {
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
func TestDomainImportsOnlyStandardLibrary(t *testing.T) {
	policyFiles(t, "internal/domain", func(path string, f *ast.File) {
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			pkg, err := build.Default.Import(name, "", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("%s imports %s; domain imports only the standard library", path, name)
			}
		}
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
