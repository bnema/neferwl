package workspace

import (
	"reflect"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"
)

// Settings of a workspace that NewFrom copies from the template; every other
// field is per-workspace state and starts fresh. Classify a new field here
// and in NewFrom.
var workspaceFields = map[string]bool{ // true: setting
	"ID": false, "Name": false, "OverviewAfter": false, "recent": false, "view": false,
	"monitor": true, "reserved": true, "size": false, "border": true, "left": false,
	"Floats": false, "floatFocus": false, "Stash": false, "stashAt": false, "stashView": false,
	"stashFocus": false, "stashHidden": false, "stashWidth": true, "stashGap": true,
	"hiddenFullscreen": false, "stashOver": false, "Home": false, "HomePos": false, "TermAt": false,
	"colBuf": false, "rowBuf": false, "tileBuf": false, "zoomBuf": false,
}

// lanesFields classifies layout.Lanes: setting (copied), derived (follows
// the monitor in NewFrom) or state (fresh).
var lanesFields = map[string]string{
	"Override": "setting", "Overflow": "setting", "Gaps": "setting", "MaxColumns": "setting",
	"Output": "derived", "Usable": "derived",
	"Focus": "state", "View": "state", "Fullscreen": "state", "Columns": "state", "Maximized": "state",
	"lcache": "derived", "lcacheFor": "derived", "colSeq": "state", "spanSeq": "state",
	"lstate": "state", "lstateReady": "state",
}

func templateWorkspace() *Workspace {
	t := &Workspace{}
	t.SetOutput(200, 100)
	t.SetUsable(Rect{X: 10, Y: 5, W: 180, H: 90})
	t.SetGaps(7)
	t.SetMaxColumns(3)
	t.SetBorder(2)
	t.SetStash(30, 4)
	t.SetOverflow(OverflowFixed)
	return t
}

// dirty fills the per-workspace state of w as far as it is reachable.
func dirty(w *Workspace) {
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddWindow(3)
	w.AddWindow(4)
	w.ToggleWindowStash()
	w.NewStamp()
	w.Focus, w.View, w.Fullscreen = 1, 5, 2
	w.Maximized = []WindowID{2}
	w.AddFloating(10, 30, 20)
	w.Stash = append(w.Stash, Float{ID: 20})
	w.stashAt, w.stashFocus, w.stashHidden, w.hiddenFullscreen, w.stashOver = 1, true, true, 20, 2
	w.recent = []WindowID{1, 2}
	w.view.Off, w.stashView.Off, w.left = 3, 4, 3
	w.size = [2]int{50, 50}
	w.Name, w.Home, w.HomePos, w.TermAt = "dev", "HDMI-1", 2, time.Unix(1, 0)
	w.OverviewAfter = &Workspace{}
	w.colBuf, w.rowBuf, w.tileBuf, w.zoomBuf = make([]Rect, 1), make([]Rect, 1), make([]Placement, 1), make([]Placement, 1)
}

func TestNewFromCopiesOnlySettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tpl := templateWorkspace()
		dirty(tpl)
		before := *tpl
		before.Columns = slices.Clone(tpl.Columns)
		w := NewFrom(tpl, 9)

		if w.ID != 9 {
			t.Fatalf("ID %d", w.ID)
		}
		if w.monitor != tpl.monitor || w.reserved != tpl.reserved || w.border != tpl.border ||
			w.stashWidth != tpl.stashWidth || w.stashGap != tpl.stashGap ||
			w.Gaps != tpl.Gaps || w.MaxColumns != tpl.MaxColumns || w.Overflow != tpl.Overflow {
			t.Fatalf("settings differ: %+v from %+v", w, tpl)
		}
		if w.Output != (Rect{W: 200, H: 100}) || w.Usable != (Rect{X: 10, Y: 5, W: 180, H: 90}) {
			t.Fatalf("output %+v usable %+v", w.Output, w.Usable)
		}
		if want, ok := layout.Lookup(tpl.Overflow); !ok || w.Engine() != want {
			t.Fatalf("engine %T, want %T", w.Engine(), want)
		}
		for name, setting := range workspaceFields {
			if name != "ID" && !setting && !reflect.ValueOf(w).Elem().FieldByName(name).IsZero() {
				t.Errorf("state field %s not fresh", name)
			}
		}
		for name, kind := range lanesFields {
			if kind == "state" && !reflect.ValueOf(&w.Lanes).Elem().FieldByName(name).IsZero() {
				t.Errorf("state field %s not fresh", name)
			}
		}

		w.AddWindow(50)
		w.SetGaps(1)
		w.SetBorder(9)
		w.SetOverflow(OverflowScroll)
		if !reflect.DeepEqual(before.Columns, tpl.Columns) || tpl.Gaps != 7 || tpl.border != 2 ||
			tpl.Overflow != OverflowFixed || len(tpl.Floats) != 1 || len(tpl.Stash) != 2 {
			t.Fatalf("template changed: %+v", tpl)
		}
	})
}

// A template layout override wins over the registry in the copy, also after
// the copy's overflow changes.
func TestNewFromKeepsLayoutOverride(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tpl := templateWorkspace()
		ov := layoutmocks.NewMockLayout(t)
		tpl.Override = ov
		w := NewFrom(tpl, 3)
		if w.Override != ov || w.Engine() != layout.Layout(ov) {
			t.Fatalf("override %v engine %v", w.Override, w.Engine())
		}
		w.SetOverflow(OverflowScroll)
		if w.Override != ov || w.Engine() != layout.Layout(ov) {
			t.Fatalf("after SetOverflow: override %v engine %v", w.Override, w.Engine())
		}
	})
}

func TestNewFromSequencesRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tpl := templateWorkspace()
		dirty(tpl)
		tpl.NewStamp()
		w := NewFrom(tpl, 2)
		w.AddWindow(1)
		w.AddWindow(2)
		// A pristine workspace issues the reference keys.
		ref := templateWorkspace()
		ref.AddWindow(1)
		ref.AddWindow(2)
		if k0, k1 := w.Columns[0].Key, w.Columns[1].Key; k0 == k1 || k0 != ref.Columns[0].Key || k1 != ref.Columns[1].Key {
			t.Fatalf("column keys %d %d, want %d %d", k0, k1, ref.Columns[0].Key, ref.Columns[1].Key)
		}
		if s := w.NewStamp(); s != 1 {
			t.Fatalf("stamp %d", s)
		}
	})
}

func TestNewFromFieldsAreClassified(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		check := func(typ reflect.Type, known func(string) bool, skip string) {
			for f := range typ.Fields() {
				if f.Name == skip {
					continue
				}
				if !known(f.Name) {
					t.Errorf("%s.%s is not classified: classify it in this test and in NewFrom", typ, f.Name)
				}
			}
		}
		check(reflect.TypeFor[Workspace](), func(n string) bool { _, ok := workspaceFields[n]; return ok }, "Lanes")
		check(reflect.TypeFor[layout.Lanes](), func(n string) bool { _, ok := lanesFields[n]; return ok }, "")
		wt, lt := reflect.TypeFor[Workspace](), reflect.TypeFor[layout.Lanes]()
		for n := range workspaceFields {
			if _, ok := wt.FieldByName(n); !ok {
				t.Errorf("stale Workspace entry %s: remove it here and from NewFrom", n)
			}
		}
		for n := range lanesFields {
			if _, ok := lt.FieldByName(n); !ok {
				t.Errorf("stale Lanes entry %s: remove it here and from NewFrom", n)
			}
		}
	})
}
