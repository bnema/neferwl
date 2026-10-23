package core

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestColumnBorders(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow Overflow
		n        int
		want     bool
	}{
		{"scroll", OverflowScroll, 5, true},
		{"cascade", OverflowCascade, 5, true},
		{"fixed row", OverflowFixed, 3, true},
		{"fixed spiral", OverflowFixed, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := Workspace{Overflow: tc.overflow, MaxColumns: 3, Columns: make([]Column, tc.n)}
			if got := columnBorders(w.layout(), w.lview()); got != tc.want {
				t.Fatalf("column borders = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigRejectsUnknownLayout(t *testing.T) {
	for _, scope := range []string{"default", "output", "named"} {
		t.Run(scope, func(t *testing.T) {
			cfg := ports.Config{}
			cfg.Layout.MaxColumns = 3
			cfg.Keyboard.CmdKey = "super"
			switch scope {
			case "default":
				cfg.Layout.Overflow = "unknown"
			case "output":
				cfg.Layout.Outputs = []ports.OutputLayout{{Output: "DP-1", LayoutRules: ports.LayoutRules{Overflow: "unknown"}}}
			case "named":
				cfg.Workspaces = []ports.WorkspaceConfig{{Name: "test", LayoutRules: ports.LayoutRules{Overflow: "unknown"}}}
			}
			var c Core
			if err := c.apply(cfg); err == nil || !strings.Contains(err.Error(), `invalid overflow "unknown"`) {
				t.Fatalf("apply = %v", err)
			}
		})
	}
}

func TestConfigAcceptsEmptyOverflow(t *testing.T) {
	c, _ := indicatorCore(t)
	cfg := c.cfg
	cfg.Layout.Overflow = ""
	cfg.Layout.Outputs = []ports.OutputLayout{{Output: "DP-1"}}
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "test"}}
	if err := c.apply(cfg); err != nil {
		t.Fatalf("empty overflow must validate in every scope: %v", err)
	}
}

func TestApplyNamedResetsOldStateBeforeNewGeometry(t *testing.T) {
	w := Workspace{Overflow: OverflowCascade, MaxColumns: 3, View: 123,
		Columns: []Column{{Windows: []WindowID{1}, FullWidth: true}},
		Usable:  Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}
	w.setBandStart(0, true)
	if w.lstate == nil {
		t.Fatal("cascade state missing")
	}
	m := Monitor{Workspaces: []*Workspace{&w}, template: Workspace{Overflow: OverflowFixed, MaxColumns: 2}}
	m.applyNamed()
	if w.lstate != nil || w.MaxColumns != 2 || w.View != 0 || w.Columns[0].FullWidth {
		t.Fatalf("new layout did not settle: state=%T capacity=%d view=%d full=%v", w.lstate, w.MaxColumns, w.View, w.Columns[0].FullWidth)
	}
}

// A layout that does not claim overridesWidth must size columns like the base:
// generic loops then skip its columnWidth. This catches a forgotten flag.
func TestOverridesWidthMatchesColumnWidth(t *testing.T) {
	forEachConformance(t, func(t *testing.T, l layout, w *Workspace, _, n int, _ string) {
		if l.overridesWidth() || n == 0 {
			return
		}
		v := w.lview()
		for _, ignore := range []bool{false, true} {
			for i := range v.Columns {
				if got, want := l.columnWidth(v, i, ignore), (columnsBase{}).columnWidth(v, i, ignore); got != want {
					t.Fatalf("%s column %d (ignore %v): width %d, base %d: set overridesWidth", l.name(), i, ignore, got, want)
				}
			}
		}
	})
}

// A layout switch keeps maximization only when the new layout does not reset it:
// scroll keeps a FullWidth column, fixed and cascade clear it.
func TestApplyNamedMaximizeAcrossSwitch(t *testing.T) {
	for _, tc := range []struct {
		from, to Overflow
		wantFull bool
	}{
		{OverflowCascade, OverflowScroll, true},
		{OverflowFixed, OverflowScroll, true},
		{OverflowScroll, OverflowFixed, false},
		{OverflowScroll, OverflowCascade, false},
		{OverflowCascade, OverflowFixed, false},
		{OverflowFixed, OverflowCascade, false},
	} {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			w := Workspace{Overflow: tc.from, MaxColumns: 3,
				Columns: []Column{{Windows: []WindowID{1}, FullWidth: true}, {Windows: []WindowID{2}}},
				Usable:  Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}
			w.setOverflow(tc.from)
			m := Monitor{Workspaces: []*Workspace{&w}, template: Workspace{MaxColumns: 3}}
			m.template.setOverflow(tc.to)
			m.applyNamed()
			if w.Overflow != tc.to {
				t.Fatalf("overflow %q, want %q", w.Overflow, tc.to)
			}
			if got := w.Columns[0].FullWidth; got != tc.wantFull {
				t.Fatalf("FullWidth after %s->%s = %v, want %v", tc.from, tc.to, got, tc.wantFull)
			}
		})
	}
}
