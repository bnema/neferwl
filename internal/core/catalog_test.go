package core_test

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

// TestCommandCatalogMatchesBindActions keeps the domain catalog in lockstep
// with workspace actions without making domain depend on core source files.
func TestCommandCatalogMatchesBindActions(t *testing.T) {
	file, err := parser.ParseFile(gotoken.NewFileSet(), "workspace/workspace_actions.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]domain.CommandKind{
		// Config's explicit no-op has no workspace Action constant.
		"none": domain.CommandAction,
	}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || (decl.Tok != gotoken.CONST && decl.Tok != gotoken.VAR) {
			return true
		}
		for _, node := range decl.Specs {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typ, _ := spec.Type.(*ast.Ident)
			action := decl.Tok == gotoken.CONST && typ != nil && typ.Name == "Action"
			prefix := false
			for _, name := range spec.Names {
				prefix = prefix || strings.HasSuffix(name.Name, "Prefix") || strings.HasSuffix(name.Name, "Prefixes")
			}
			if !action && !prefix {
				continue
			}
			for _, value := range spec.Values {
				ast.Inspect(value, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != gotoken.STRING {
						return true
					}
					name, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					kind := domain.CommandAction
					if prefix {
						name = strings.TrimSpace(name)
						kind = domain.CommandActionArg
					}
					if _, exists := actions[name]; exists {
						t.Errorf("duplicate workspace action %q", name)
					}
					actions[name] = kind
					return true
				})
			}
		}
		return false
	})
	catalog := map[string]domain.CommandKind{}
	for _, spec := range domain.Commands() {
		if spec.Kind != domain.CommandAction && spec.Kind != domain.CommandActionArg {
			continue
		}
		if _, exists := catalog[spec.Name]; exists {
			t.Errorf("duplicate catalog action %q", spec.Name)
		}
		catalog[spec.Name] = spec.Kind
		if kind, ok := actions[spec.Name]; !ok || kind != spec.Kind {
			t.Errorf("catalog action %q (%v) has no matching bind action", spec.Name, spec.Kind)
		}
	}
	for name, kind := range actions {
		if got, ok := catalog[name]; !ok || got != kind {
			t.Errorf("bind action %q (%v) missing from catalog", name, kind)
		}
	}
}
