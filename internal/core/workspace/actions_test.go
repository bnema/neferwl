package workspace

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"slices"
	"strconv"
	"testing"
)

// TestActionsListsEveryConstant keeps Actions in step with the Action
// constants declared next to it.
func TestActionsListsEveryConstant(t *testing.T) {
	file, err := parser.ParseFile(gotoken.NewFileSet(), "workspace_actions.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []Action
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != gotoken.CONST {
			continue
		}
		for _, node := range gen.Specs {
			spec := node.(*ast.ValueSpec)
			if typ, _ := spec.Type.(*ast.Ident); typ == nil || typ.Name != "Action" {
				continue
			}
			for _, value := range spec.Values {
				lit := value.(*ast.BasicLit)
				name, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				declared = append(declared, Action(name))
			}
		}
	}
	got := Actions()
	slices.Sort(got)
	slices.Sort(declared)
	if !slices.Equal(got, declared) {
		t.Fatalf("Actions() = %v, constants %v", got, declared)
	}
}
