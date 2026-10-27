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
		{"ports", []string{"domain"}},
		{"core", []string{"domain", "ports", "core/geom", "core/layout", "core/motion", "core/workspace"}},
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
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != timeName {
				return true
			}
			switch sel.Sel.Name {
			case "Now", "Since", "Until", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick", "Sleep":
				t.Errorf("%s uses time.%s; use ports.Clock", path, sel.Sel.Name)
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

// TestAdapterImports checks every production source file, including inactive
// build tags. Adapters may import their own subpackages and shared libraries.
func TestAdapterImports(t *testing.T) {
	const base = "github.com/bnema/neferwl/"
	const adapters = base + "internal/adapters/"
	// Keep the shared-library allowlist here, not in the build-tag-sensitive
	// go list output. All other cross-adapter dependencies must use ports.
	libraries := strings.Fields("drm config sched seat watchdog fdwait busretry overlaydraw outputkit/routing outputkit/cursors xcursor outputkit/capture outputkit/corewake outputkit/presented outputkit/surfaces captureallow clock hdrcursor logging peerexe sessionsecurity syncfile workspaceid xkb")
	err := filepath.WalkDir("internal/adapters", func(dir string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		policyFiles(t, dir, func(path string, f *ast.File) {
			from := base + filepath.ToSlash(filepath.Dir(path))
			for _, imp := range f.Imports {
				name, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(name, adapters) || strings.HasPrefix(name, from+"/") {
					continue
				}
				allowed := false
				for _, lib := range libraries {
					allowed = allowed || name == adapters+lib
				}
				if !allowed {
					t.Errorf("%s imports forbidden adapter %s; use ports or a shared library", path, name)
				}
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
