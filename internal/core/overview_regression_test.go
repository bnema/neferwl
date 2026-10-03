package core

import "testing"

func TestOverviewStashVerticalSelectsFront(t *testing.T) {
	for _, accept := range []bool{false, true} {
		m := stackMonitor()
		w := m.Current()
		w.FocusID(1)
		w.ToggleWindowStash()
		w.FocusID(9)
		m.ToggleOverview()
		m.OverviewMove(-1, 0) // float to columns
		m.OverviewMove(-1, 0) // first column to stash
		if m.cardAt(w) < 0 {
			t.Fatal("not in stash")
		}
		m.OverviewMove(0, 1)
		p := previewOf(t, m.Layout(), 9)
		if !p.Focused || p.Peek || m.cardAt(w) >= 0 {
			t.Fatalf("vertical selection: %+v, stash %d", p, m.cardAt(w))
		}
		if e := m.Apply(ActionCloseWindow); e.Close != 9 {
			t.Fatalf("close target %d, want 9", e.Close)
		}
		if accept {
			m.ToggleOverview()
			if id, _ := w.Focused(); id != 9 || !w.floatFocus {
				t.Fatalf("accepted focus %d, stash hidden %v", id, w.stashHidden)
			}
		}
	}
}

func TestOverviewSingleCoveringFloat(t *testing.T) {
	for _, stash := range []bool{false, true} {
		for _, neighbor := range []bool{false, true} {
			for _, click := range []bool{false, true} {
				m := monitor()
				m.SetOutput(300, 200)
				w := m.Current()
				if stash {
					w.AddWindow(1)
					w.ToggleWindowStash()
					w.ToggleStashVisible()
				}
				m.AddFloating(9, 300, 200)
				if neighbor {
					m.Focus(1)
					m.AddFloating(8, 300, 200)
					m.Focus(0)
				}
				m.ToggleOverview()
				p := previewOf(t, m.Layout(), 9)
				if p.Hidden || p.Preview <= 0 || p.Rect.W <= 0 || p.Rect.H <= 0 || !p.Focused {
					t.Fatalf("single float preview %+v", p)
				}
				if neighbor {
					q := previewOf(t, m.Layout(), 8)
					if q.Hidden || q.Preview <= 0 || q.Focused || !q.Peek {
						t.Fatalf("neighbor preview %+v", q)
					}
				}
				if click {
					id := m.overviewAt(float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2))
					if id != 9 {
						t.Fatalf("hit %d, want 9", id)
					}
					m.OverviewPick(id)
				} else {
					m.ToggleOverview()
				}
				if id, _ := w.Focused(); id != 9 || m.ov.open {
					t.Fatalf("accepted focus %d, overview %v", id, m.ov.open)
				}
			}
		}
	}
}

func TestOverviewCloseTargetsFront(t *testing.T) {
	m := stackMonitor()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	if e := m.Apply(ActionCloseWindow); e.Close != 3 {
		t.Fatalf("close target %d, want selected column 3", e.Close)
	}
}

func TestOverviewTargetAfterUnmaximizeMutation(t *testing.T) {
	for _, add := range []bool{false, true} {
		m := maximizedOverview()
		w := m.Current()
		m.ToggleOverview()
		m.OverviewMove(0, -1)
		m.OverviewMove(1, 0)
		if m.ov.selected != 3 {
			t.Fatalf("setup selection %d, want 3", m.ov.selected)
		}
		if add {
			w.AddWindow(7)
		} else {
			w.RemoveWindow(2)
		}
		var highlighted WindowID
		for _, p := range m.Layout() {
			if p.Focused && !p.Hidden {
				if highlighted != 0 {
					t.Fatal("multiple highlighted previews")
				}
				highlighted = p.ID
			}
		}
		if highlighted == 0 || m.overviewTarget() != highlighted || m.Apply(ActionCloseWindow).Close != highlighted {
			t.Fatalf("add %v: highlighted %d, target %d, close %d", add, highlighted, m.overviewTarget(), m.Apply(ActionCloseWindow).Close)
		}
		m.ToggleOverview()
		if id, _ := w.Focused(); id != highlighted {
			t.Fatalf("accepted %d, want highlighted %d", id, highlighted)
		}
	}
}

func TestOverviewWindowMutationsDisabled(t *testing.T) {
	actions := []Action{
		ActionMoveColumnLeft, ActionMoveColumnRight, ActionCycleColumnWidth,
		ActionMaximizeColumn, ActionToggleFullscreen, ActionToggleWindowStash,
		ActionToggleStashVisible, ActionConsumeOrExpelLeft,
		ActionConsumeOrExpelRight, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight,
		ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown,
	}
	for _, a := range actions {
		t.Run(string(a), func(t *testing.T) {
			m := stackMonitor()
			m.ToggleOverview()
			m.OverviewMove(0, -1)
			before := m.Layout()
			m.Apply(a)
			after := m.Layout()
			if len(before) != len(after) {
				t.Fatalf("layout length changed: %d -> %d", len(before), len(after))
			}
			for i := range before {
				if before[i] != after[i] {
					t.Fatalf("layout changed: %+v -> %+v", before[i], after[i])
				}
			}
		})
	}
}
