package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
)

func TestCommands(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range Commands() {
		if seen[spec.Name] || spec.Name == "" || spec.Summary == "" {
			t.Fatalf("invalid/duplicate spec: %+v", spec)
		}
		seen[spec.Name] = true
		got, ok := LookupCommand(spec.Name)
		if !ok || got != spec {
			t.Fatalf("lookup %q = %+v, %v", spec.Name, got, ok)
		}
		if spec.Kind == CommandAction || spec.Kind == CommandActionArg {
			if spec.Caps != CapAct {
				t.Errorf("%s caps = %v", spec.Name, spec.Caps)
			}
		}
		if spec.Kind == CommandActionArg {
			if spec.Arg == "" {
				t.Errorf("missing argument description: %s", spec.Name)
			}
			got, ok = LookupCommand(spec.Name + " example with spaces")
			if !ok || got != spec {
				t.Errorf("prefix lookup failed: %s", spec.Name)
			}
		}
	}
	for _, tc := range []struct {
		name string
		kind CommandKind
		caps Caps
	}{
		{"state", CommandQuery, CapRead}, {"key", CommandInject, CapInput},
		{"type", CommandInject, CapInput}, {"pointer", CommandInject, CapInput},
		{"swipe", CommandInject, CapInput}, {"shot", CommandShot, CapCapture},
	} {
		spec, ok := LookupCommand(tc.name)
		if !ok || spec.Kind != tc.kind || spec.Caps != tc.caps {
			t.Errorf("%s = %+v", tc.name, spec)
		}
	}
	for _, text := range []string{"", "unknown", "focus-workspaceX 2", "focus-workspace ", "spawn\tfoot", "quit now", "state extra"} {
		if _, ok := LookupCommand(text); ok {
			t.Errorf("accepted %q", text)
		}
	}
	copy := Commands()
	copy[0].Name = "changed"
	if Commands()[0].Name == "changed" {
		t.Fatal("catalog aliases returned slice")
	}
	if CapsAll != CapRead|CapAct|CapInput|CapCapture|CapAdmin {
		t.Fatal("CapsAll")
	}
}

// Parse source rather than importing an adapter into domain tests. This also
// catches future config action additions without duplicating its action list.
func TestCatalogCoversConfigAndWorkspaceActions(t *testing.T) {
	for _, tc := range []struct{ path, symbol string }{
		{"../adapters/config/config.go", "actions"},
		{"../core/workspace/workspace_actions.go", ""},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), tc.path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			selected := tc.symbol != "" && len(spec.Names) == 1 && spec.Names[0].Name == tc.symbol
			if tc.symbol == "" {
				typ, ok := spec.Type.(*ast.Ident)
				selected = ok && typ.Name == "Action"
			}
			if !selected {
				return true
			}
			for _, value := range spec.Values {
				ast.Inspect(value, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					name, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					count++
					got, ok := LookupCommand(name)
					if !ok || got.Kind != CommandAction || got.Caps != CapAct {
						t.Errorf("missing action %q", name)
					}
					return true
				})
			}
			return false
		})
		if count == 0 {
			t.Fatalf("no actions found in %s", tc.path)
		}
	}
}

func TestWaitLevel(t *testing.T) {
	for _, level := range []WaitLevel{WaitCommitted, WaitSettled, WaitPresented} {
		got, err := ParseWaitLevel(level.String())
		if err != nil || got != level {
			t.Errorf("round trip %v: %v %v", level, got, err)
		}
	}
	for _, text := range []string{"", "idle", "SETTLED", "settled "} {
		if _, err := ParseWaitLevel(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	if DefaultWait != WaitSettled || WaitLevel(99).String() != "WaitLevel(99)" {
		t.Fatal("wait defaults/unknown")
	}
}

func TestUntil(t *testing.T) {
	window := WindowState{ID: 12, AppID: "foot"}
	state := State{Window: &window, Windows: []WindowState{window}}
	for _, tc := range []struct {
		text         string
		want         Until
		holds, empty bool
	}{
		{"window-mapped app_id=foot", Until{Kind: UntilWindowMapped, AppID: "foot"}, true, false},
		{"window-mapped app_id=x", Until{Kind: UntilWindowMapped, AppID: "x"}, false, false},
		{"focused app_id=foot", Until{Kind: UntilFocused, AppID: "foot"}, true, false},
		{"focused app_id=x", Until{Kind: UntilFocused, AppID: "x"}, false, false},
		{"unmapped id=12", Until{Kind: UntilUnmapped, Window: 12}, false, true},
		{"unmapped id=13", Until{Kind: UntilUnmapped, Window: 13}, true, true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := ParseUntil(tc.text)
			if err != nil || got != tc.want {
				t.Fatalf("parse = %+v, %v", got, err)
			}
			if got.Holds(state) != tc.holds || got.Holds(State{}) != tc.empty {
				t.Fatal("predicate result")
			}
		})
	}
	for _, text := range []string{"", "idle", "focused", "focused id=12", "window-mapped app_id=", "unmapped app_id=x", "unmapped id=0", "unmapped id=-1", "unmapped id=18446744073709551616", "unmapped id=x", "focused app_id=x extra"} {
		if _, err := ParseUntil(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, u := range []Until{{}, {Kind: UntilFocused}, {Kind: UntilUnmapped}, {Kind: UntilKind(99)}} {
		if u.Holds(State{}) {
			t.Errorf("invalid predicate holds: %+v", u)
		}
	}
}

func TestDiffState(t *testing.T) {
	a, b := WindowState{ID: 1}, WindowState{ID: 2}
	before := State{Window: &a, Windows: []WindowState{a}, Outputs: []OutputState{{Name: "DP-1", Active: 1, WorkspaceID: 1}}}
	after := State{Window: &b, Windows: []WindowState{b}, Outputs: []OutputState{{Name: "DP-1", Active: 2, WorkspaceID: 2}, {Name: "DP-2", Workspace: "dev", WorkspaceID: 3}}}
	for _, tc := range []struct {
		name          string
		before, after State
		want          StateDiff
	}{
		{"empty", State{}, State{}, StateDiff{}},
		{"unchanged", before, before, StateDiff{FocusFrom: 1, FocusTo: 1}},
		{"changes", before, after, StateDiff{Mapped: []WindowID{2}, Unmapped: []WindowID{1}, FocusFrom: 1, FocusTo: 2, FocusChanged: true, Workspaces: []string{"DP-1:2", "DP-2:dev"}}},
		{"removed", before, State{}, StateDiff{Unmapped: []WindowID{1}, FocusFrom: 1, FocusChanged: true}},
		{"added", State{}, before, StateDiff{Mapped: []WindowID{1}, FocusTo: 1, FocusChanged: true, Workspaces: []string{"DP-1:1"}}},
		{"workspace identity", State{Outputs: []OutputState{{Name: "DP-1", Active: 1, WorkspaceID: 1}}}, State{Outputs: []OutputState{{Name: "DP-1", Active: 1, WorkspaceID: 9}}}, StateDiff{Workspaces: []string{"DP-1:1"}}},
		{"metadata ignored", State{Windows: []WindowState{{ID: 1}}}, State{Windows: []WindowState{{ID: 1, AppID: "new"}}}, StateDiff{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiffState(tc.before, tc.after); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
