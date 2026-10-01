package core

import "testing"

func TestFullscreenFloatReturnPreservesDialogFocus(t *testing.T) {
	for _, covering := range []bool{false, true} {
		m := monitor()
		m.SetOverflow(OverflowFixed)
		m.AddWindow(1)
		size := 30
		if covering {
			size = 100
		}
		m.AddFloating(2, size, size)
		w := m.Current()
		if covering {
			w.FocusID(1)
		}
		m.SetFullscreen(2, true)
		m.show(w)
		m.AddFloating(3, 20, 20)
		m.SetFullscreen(2, false)
		if id, _ := w.Focused(); id != 3 || !w.floatFocus || w.Floats[len(w.Floats)-1].ID != 3 {
			t.Fatalf("covering %v: focus %d, float focus %v, floats %+v", covering, id, w.floatFocus, w.Floats)
		}
	}
}

func TestDemotedFloatFullscreenUserReturn(t *testing.T) {
	m := monitor()
	m.SetOverflow(OverflowFixed)
	m.AddWindow(1)
	m.AddFloating(2, 100, 80)
	w := m.Current()
	w.FocusID(2)
	m.SetFullscreen(2, true)
	w.focusCover()
	m.SetFullscreen(2, false)
	if id, _ := w.Focused(); id != 2 || !w.floatFocus || w.Floats[0].below {
		t.Fatalf("returned focus %d floats %+v", id, w.Floats)
	}
}

func TestSetNamedDemotedFloatKeepsColumnActions(t *testing.T) {
	m := monitor()
	m.SetNamed([]NamedWorkspace{{Name: "work"}})
	m.AddWindow(1)
	m.AddFloating(2, 100, 80)
	m.Current().FocusID(1)
	m.SetNamed(nil)
	w := m.Current()
	if id, _ := w.Focused(); id != 1 || w.onFloat() || w.floatFocus {
		t.Fatalf("focus %d float focus %v floats %+v", id, w.floatFocus, w.Floats)
	}
	w.ToggleFullWidth()
	if !w.Columns[w.Focus].FullWidth {
		t.Fatal("column action was blocked")
	}
}

func TestFloatOnlyWorkspaceDirectionalExit(t *testing.T) {
	for _, dir := range []int{-1, 1} {
		m := monitor()
		m.AddWindow(1)
		m.Focus(1)
		m.AddFloating(2, 20, 20)
		m.Focus(2)
		m.AddWindow(3)
		m.Focus(1)
		a := ActionFocusWindowUp
		if dir > 0 {
			a = ActionFocusWindowDown
		}
		m.Apply(a)
		if m.Active != 1+dir {
			t.Fatalf("direction %d stayed on workspace %d", dir, m.Active)
		}
	}
}

func TestOverviewMaximizedPreviewAfterAnchorRemoval(t *testing.T) {
	m := maximizedOverview()
	w := m.Current()
	w.Columns[1].Windows = append(w.Columns[1].Windows, 5)
	// Closing the maximization anchor leaves the surviving column maximized.
	w.RemoveWindow(2)
	m.ToggleOverview()
	p := previewOf(t, m.Layout(), 5)
	if p.Hidden || p.Preview <= 0 {
		t.Fatalf("preview %+v", p)
	}
	real := previewOf(t, w.Layout(), 5)
	if p.Rect.W != int(float64(real.Rect.W)*p.Preview+0.5) || p.Rect.H != int(float64(real.Rect.H)*p.Preview+0.5) {
		t.Fatalf("preview %+v real %+v", p, real)
	}
}

func TestOverviewPickStackedColumnWindow(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		m := maximizedOverview()
		w := m.Current()
		i := w.Focus
		if hidden {
			i = 2
		}
		w.Columns[i].Windows = append(w.Columns[i].Windows, 6)
		m.ToggleOverview()
		m.OverviewPick(6)
		if id, _ := w.Focused(); id != 6 || m.ov.open {
			t.Fatalf("hidden %v: picked focus %d, overview %v", hidden, id, m.ov.open)
		}
	}
}

func TestOverviewTargetAfterColumnActivation(t *testing.T) {
	for _, maximized := range []bool{false, true} {
		m := overviewMonitor()
		w := m.Current()
		w.AddWindow(5)
		w.ConsumeOrExpel(-1)
		w.FocusID(3)
		if maximized {
			m.SetOverflow(OverflowFixed)
			w.ToggleFullWidth()
		}
		m.ToggleOverview()
		if maximized && w.stack()[0].kind != stackColumn {
			t.Fatalf("expected maximized column card: %+v", w.stack())
		}
		w.Activate(5)
		var highlighted WindowID
		for _, p := range m.Layout() {
			if p.Focused && !p.Hidden {
				highlighted = p.ID
			}
		}
		if highlighted == 0 || m.overviewTarget() != highlighted || m.Apply(ActionCloseWindow).Close != highlighted {
			t.Fatalf("maximized %v: highlight %d target %d", maximized, highlighted, m.overviewTarget())
		}
		m.ToggleOverview()
		if id, _ := w.Focused(); id != highlighted {
			t.Fatalf("accepted %d want %d", id, highlighted)
		}
	}
}
