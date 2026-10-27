package core

import (
	"testing"
	"testing/synctest"
)

func TestCascadeStashRestoreKeepsReloadClearsMaximization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(900, 600)
		for i := WindowID(1); i <= 3; i++ {
			m.AddWindow(i)
		}
		m.Current().FocusID(1)
		m.Current().ToggleFullWidth()
		m.Current().FocusID(3)
		m.SetOverflow(OverflowCascade)
		for _, c := range m.Current().Columns {
			if c.FullWidth {
				t.Fatal("reload retained maximization")
			}
		}

	})
}

// Leaving cascade drops the band offset: scroll realigns on the focus
// instead of keeping it as a horizontal offset past the last column.
func TestReloadCascadeToScrollResetsView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(900, 600)
		m.SetMaxColumns(3)
		m.SetOverflow(OverflowCascade)
		for i := WindowID(1); i <= 4; i++ {
			m.AddWindow(i)
		}
		if m.Current().View != 600 {
			t.Fatalf("cascade view=%d, want 600", m.Current().View)
		}
		m.SetOverflow(OverflowScroll)
		if got := m.Current().View; got != 300 {
			t.Fatalf("scroll view=%d, want 300", got)
		}

	})
}

// Up/down stop at the first and last band: cascade workspaces sit side by
// side, so a vertical key never switches workspace.
func TestCascadeFocusWindowStopsAtEdgeBands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(900, 600)
		m.SetMaxColumns(3)
		m.SetOverflow(OverflowCascade)
		for i := WindowID(1); i <= 4; i++ {
			m.AddWindow(i)
		}
		for _, tc := range []struct {
			focus WindowID
			a     Action
		}{{4, ActionFocusWindowDown}, {1, ActionFocusWindowUp}} {
			m.Current().FocusID(tc.focus)
			m.Apply(tc.a)
			if id, _ := m.Current().Focused(); m.Active != 0 || id != tc.focus {
				t.Fatalf("%s from %d: active=%d focus=%d", tc.a, tc.focus, m.Active, id)
			}
		}

	})
}

func TestCascadeGestureValidityDoesNotAllocate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(900, 600)
		m.SetOverflow(OverflowCascade)
		for i := WindowID(1); i <= 7; i++ {
			m.AddWindow(i)
		}
		g := &swipeGesture{ws: m.Current(), points: m.Current().Engine().SnapPoints(m.Current().Snapshot(), nil), horizontal: false, opens: m.overviewOpens}
		if n := testing.AllocsPerRun(100, func() {
			if g.columnsChanged(m) {
				t.Fatal("unchanged gesture invalidated")
			}
		}); n != 0 {
			t.Fatalf("allocations=%v", n)
		}

	})
}

func TestSlideLayoutAxisOffset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewMonitor()
		m.SetOutput(900, 600)
		m.Current().Overflow = OverflowCascade
		m.AddWindow(1)
		m.Focus(1)
		m.AddWindow(2)
		m.Current().Overflow = OverflowScroll
		m.Focus(0)
		_, m.switchAxis = m.Current().Engine().Axes()
		m.switchView.Off = .5
		if p := previewOf(t, m.layoutInto(nil), 1); p.Rect.X != -450 || p.Rect.Y != 0 {
			t.Fatalf("cascade source offset=%v", p.Rect)
		}
		m.stopSwitch()
		m.Focus(1)
		_, m.switchAxis = m.Current().Engine().Axes()
		m.switchView.Off = -.5
		if p := previewOf(t, m.layoutInto(nil), 2); p.Rect.X != 0 || p.Rect.Y != 300 {
			t.Fatalf("scroll source offset=%v", p.Rect)
		}

	})
}
