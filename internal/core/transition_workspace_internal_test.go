package core

import (
	"math"
	"testing"
	"testing/synctest"
	"time"
)

// Real monitor/workspace fixtures exercise overflow clipping, not synthetic shots.
func TestOverviewWorkspaceEntranceGroup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		entering, leaving Overflow
		horizontal        bool
	}{
		{"cascade", OverflowCascade, OverflowCascade, true},
		{"scroll", OverflowScroll, OverflowScroll, false},
		{"mixed-new-scroll-axis", OverflowScroll, OverflowCascade, false},
		{"mixed-new-cascade-axis", OverflowCascade, OverflowScroll, true},
		{"fixed", OverflowFixed, OverflowFixed, false},
		{"mixed-fixed-to-cascade", OverflowCascade, OverflowFixed, true},
		{"mixed-cascade-to-fixed", OverflowFixed, OverflowCascade, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, ic, sc := overviewRig(t)
				m := sc.mon
				m.Current().Overflow = tc.entering
				m.AddWindow(4)
				m.AddWindow(5)
				m.Focus(1)
				m.Current().Overflow = tc.leaving
				settleShown(t, c)
				toggleOverview(c, sc, ic.now)
				frame(t, c, ic, 5*time.Second)
				shots := c.snapshot(ic.now)
				m.Focus(0)
				c.transition(shots, ic.now)
				indicatorScene(t, c)
				if m.Current() != m.Workspaces[0] {
					t.Fatal("focus did not change immediately")
				}
				area := m.Current().OverviewArea()
				want := -float64(area.H) * 0.12
				if tc.horizontal {
					want = -float64(area.W) * 0.12
				}
				count := 0
				for _, p := range sc.settledLayout {
					if p.Hidden || !m.Current().Has(p.ID) {
						continue
					}
					count++
					rm := sc.rects[p.ID]
					got, cross := rm.dy, rm.dx
					if tc.horizontal {
						got, cross = rm.dx, rm.dy
					}
					wasDrawn := false
					for _, r := range shots[0].rects {
						if r.id == p.ID {
							wasDrawn = true
						}
					}
					if !wasDrawn && rm.df != 1 {
						t.Errorf("new tile %d did not fade in: %g", p.ID, rm.df)
					}
					if wasDrawn && rm.df != 0 {
						t.Errorf("preview tile %d unexpectedly fades: %g", p.ID, rm.df)
					}
					if got != want || cross != 0 {
						t.Errorf("tile %d offset (%g,%g), want axis %g, cross 0", p.ID, rm.dx, rm.dy, want)
					}
				}
				if count != 4 {
					t.Fatalf("drew %d entering tiles, want 4", count)
				}
				frame(t, c, ic, 150*time.Millisecond)
				for id, rm := range sc.rects {
					if m.Current().Has(id) && math.Abs(rm.dx+rm.dy) > math.Abs(want)*0.1 {
						t.Errorf("tile %d still more than 10%% away after 150ms: (%g,%g)", id, rm.dx, rm.dy)
					}
				}
				frame(t, c, ic, 5*time.Second)
				if len(sc.rects) != 0 {
					t.Fatal("workspace motions did not settle")
				}

			})
		})
	}
}

func TestOverviewWorkspaceHiddenTilesLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		m := sc.mon
		m.Current().Overflow = OverflowCascade
		m.AddWindow(4)
		m.AddWindow(5)
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		shots := c.snapshot(ic.now)
		before := append([]rectShot(nil), shots[0].rects...)
		old := m.Current()
		m.Focus(1)
		m.Current().Overflow = OverflowCascade
		c.transition(shots, ic.now)
		indicatorScene(t, c)
		left := 0
		for _, r := range before {
			if !old.Has(r.id) {
				continue
			}
			p := settledOf(t, sc, r.id)
			rm := sc.rects[r.id]
			if p.Hidden {
				left++
				if !p.Leaving || !rm.leaving {
					t.Errorf("tile %d vanished instead of leaving", r.id)
				}
				want := int(math.Round(float64(m.Current().OverviewArea().W) * overviewWorkspaceTravel))
				if rm.left.Rect.X != r.rect.X-want || rm.dx != float64(want)+r.off.x || rm.dy != r.off.y {
					t.Errorf("tile %d does not share the exit offset: %+v", r.id, rm)
				}
				if rm.df != -1 {
					t.Errorf("tile %d does not fade out: %g", r.id, rm.df)
				}
			} else if rm.df != 0 {
				t.Errorf("visible neighbor %d fades: %g", r.id, rm.df)
			}
		}
		if left == 0 {
			t.Fatal("fixture has no clipped leaving tiles")
		}

	})
}

func TestOverviewWorkspaceSelectionKeepsPerWindowMotion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		sc.mon.Current().Overflow = OverflowCascade
		sc.mon.Current().MaxColumns = 1
		sc.mon.AddWindow(4)
		sc.mon.AddWindow(5)
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		shots := c.snapshot(ic.now)
		old := shots[0].ws
		sc.mon.OverviewMove(0, -1)
		if sc.mon.Current() != old {
			t.Fatal("fixture moved workspace instead of selection")
		}
		c.transition(shots, ic.now)
		indicatorScene(t, c)
		moved := 0
		for _, r := range shots[0].rects {
			p := settledOf(t, sc, r.id)
			if p.Hidden {
				continue
			}
			rm := sc.rects[r.id]
			if rm.dx != float64(r.rect.X-p.Rect.X)+r.off.x || rm.dy != float64(r.rect.Y-p.Rect.Y)+r.off.y {
				t.Errorf("tile %d no longer starts at old shown rect", r.id)
			}
			if rm.dx != 0 || rm.dy != 0 {
				moved++
			}
		}
		if moved == 0 {
			t.Fatal("fixture did not move any cards")
		}

	})
}

func TestOverviewWorkspaceRetargetCarriesVelocity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		shots := c.snapshot(ic.now)
		sc.mon.Focus(1)
		c.transition(shots, ic.now)
		frame(t, c, ic, 30*time.Millisecond)
		shots = c.snapshot(ic.now)
		sc.mon.Focus(0)
		c.transition(shots, ic.now)
		moving := 0
		for _, r := range shots[0].rects {
			if !sc.mon.Current().Has(r.id) {
				continue
			}
			rm := sc.rects[r.id]
			_, v := rm.sample(ic.now)
			if v.x != r.vel.x || v.y != r.vel.y {
				t.Errorf("tile %d lost velocity: %+v -> %+v", r.id, r.vel, v)
			}
			if v.x != 0 || v.y != 0 {
				moving++
			}
		}
		if moving == 0 {
			t.Fatal("fixture has no running motion")
		}
		frame(t, c, ic, 5*time.Second)
		if len(sc.rects) != 0 {
			t.Fatal("retarget did not settle")
		}

	})
}

func TestOverviewWorkspaceIncludesStashAndFloats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		m := sc.mon
		w := m.Current()
		w.Overflow = OverflowCascade
		m.AddWindow(4)
		w.ToggleWindowStash()
		m.AddWindow(5)
		w.ToggleFloating()
		m.Focus(1)
		m.Current().Overflow = OverflowCascade
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		shots := c.snapshot(ic.now)
		m.Focus(0)
		c.transition(shots, ic.now)
		indicatorScene(t, c)
		for _, id := range []WindowID{1, 2, 4, 5} {
			p := settledOf(t, sc, id)
			if p.Hidden {
				t.Fatalf("tile %d hidden in fixture", id)
			}
			rm := sc.rects[id]
			if rm.dx != -float64(w.OverviewArea().W)*overviewWorkspaceTravel || rm.dy != 0 {
				t.Errorf("tile %d not in group: %+v", id, rm)
			}
		}

	})
}

// Switching again while the previous switch's slide still runs: the cards the
// first switch sent away (leaving, fading out) come back as an entering group
// that keeps their speed and the fade they had reached, instead of starting
// from rest and fully faded. Only a layout whose overview clips cards has
// leaving cards (cascade bands under the lane); scroll and fixed show every
// card of a workspace, so their cards just retarget (the entrance test).
func TestOverviewWorkspaceSwitchDuringRunningSlide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		m := sc.mon
		for w := range m.all() {
			w.Overflow = OverflowCascade
		}
		m.AddWindow(4)
		m.AddWindow(5)
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		home := m.Current()
		shots := c.snapshot(ic.now)
		m.Focus(1)
		c.transition(shots, ic.now)
		frame(t, c, ic, 40*time.Millisecond)
		var leaving WindowID
		var speed rectOffsets
		var fade float64
		for id, rm := range sc.rects {
			if rm.leaving && home.Has(id) {
				leaving = id
				_, speed = rm.sample(ic.now)
				fade = rm.df
				sampleComponent(rm.fade, &fade, new(float64), ic.now)
				break
			}
		}
		if leaving == 0 {
			t.Fatal("fixture: no card of the left workspace is leaving")
		}
		if speed.x == 0 && speed.y == 0 || fade >= 0 || fade <= -1 {
			t.Fatalf("fixture: the leaving card must be moving and part way faded: speed (%g,%g), fade %g", speed.x, speed.y, fade)
		}
		shots = c.snapshot(ic.now)
		m.Focus(0)
		c.transition(shots, ic.now)
		indicatorScene(t, c)
		rm := sc.rects[leaving]
		if rm.leaving {
			t.Fatal("the card is still leaving after its workspace came back")
		}
		// It re-enters from where the fade had taken it: partly faded, not fully.
		if rm.df <= 0 || rm.df >= 1 {
			t.Errorf("fade offset %g: want the partly faded level the first slide reached, between 0 and 1", rm.df)
		}
		if _, v := rm.sample(ic.now); v.x != speed.x || v.y != speed.y {
			t.Errorf("speed (%g,%g), want the leaving motion's (%g,%g)", v.x, v.y, speed.x, speed.y)
		}
		frame(t, c, ic, 5*time.Second)
		if len(sc.rects) != 0 {
			t.Fatalf("%d motions did not settle", len(sc.rects))
		}

	})
}

// A stash carousel that wraps, with a card selected, belongs to one workspace:
// switching to a workspace with its own wrapping carousel is a group slide
// for every card, the wrap fade (a selection move inside one workspace)
// never applies, and none of the landing carousel's cards starts faded in.
func TestOverviewWorkspaceSwitchCarouselWrapIsGroupSlide(t *testing.T) {
	for _, l := range registeredLayouts() {
		t.Run(string(l.Name()), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, ic, sc := overviewRig(t)
				m := sc.mon
				stash := func(w *Workspace, first WindowID) {
					for id := first; id < first+3; id++ {
						w.AddWindow(id)
						w.FocusID(id)
						w.ToggleWindowStash()
					}
				}
				stash(m.Workspaces[0], 10)
				m.Focus(1)
				stash(m.Workspaces[1], 20)
				m.Focus(0)
				for _, w := range m.Workspaces {
					w.Overflow = l.Name()
				}
				from, to := m.Workspaces[0], m.Workspaces[1]
				if !from.WrapsCards() || !to.WrapsCards() {
					t.Fatalf("fixture: wraps %v / %v", from.WrapsCards(), to.WrapsCards())
				}
				settleShown(t, c)
				toggleOverview(c, sc, ic.now)
				frame(t, c, ic, 5*time.Second)
				m.selectCard(from, 1)
				if m.cardAt(from) != 1 {
					t.Fatal("fixture: no card selected")
				}
				frame(t, c, ic, 5*time.Second)
				switchAndCheck(t, c, ic, sc, func() { m.Focus(1) }, 1)
				cards := 0
				for _, p := range sc.settledLayout {
					if p.Hidden || to.StashIndex(p.ID) < 0 {
						continue
					}
					cards++
					if rm := sc.rects[p.ID]; rm.df != 0 {
						t.Errorf("card %d fades in (%g): the wrap fade must not apply to a switch", p.ID, rm.df)
					}
				}
				if cards == 0 {
					t.Fatal("fixture: the landing workspace shows no stash card")
				}

			})
		})
	}
}

func TestOverviewLeavingNeighborReappearsContinuously(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		m := sc.mon
		m.AddWindow(4)
		m.Focus(2)
		m.AddWindow(5)
		m.Focus(0)
		for w := range m.all() {
			w.Overflow = OverflowCascade
		}
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		frame(t, c, ic, 5*time.Second)
		shots := c.snapshot(ic.now)
		m.Focus(2)
		c.transition(shots, ic.now)
		frame(t, c, ic, 40*time.Millisecond)
		shots = c.snapshot(ic.now)
		m.Focus(1)
		var id WindowID
		for _, p := range m.Layout() {
			if !p.Hidden && sc.rects[p.ID].leaving && m.Workspaces[0].Has(p.ID) {
				id = p.ID
				break
			}
		}
		if id == 0 {
			t.Fatal("no leaving A card")
		}
		// Switch between frames: the motion must resume from where it would be
		// now, not from the last drawn offsets.
		ic.now = ic.now.Add(8 * time.Millisecond)
		prev := sc.rects[id]
		wantOff, wantVel := prev.sample(ic.now)
		wantFade, wantFadeV := prev.df, 0.0
		sampleComponent(prev.fade, &wantFade, &wantFadeV, ic.now)
		c.transition(shots, ic.now)
		rm := sc.rects[id]
		if rm.leaving {
			t.Fatal("reappeared card still leaving")
		}
		base := Rect{}
		for _, p := range m.Layout() {
			if p.ID == id {
				base = p.Rect
			}
		}
		gotOff := rectOffsets{rm.dx, rm.dy, rm.dw, rm.dh}
		wantAbs := rectOffsets{
			float64(prev.left.Rect.X-base.X) + wantOff.x, float64(prev.left.Rect.Y-base.Y) + wantOff.y,
			float64(prev.left.Rect.W-base.W) + wantOff.w, float64(prev.left.Rect.H-base.H) + wantOff.h,
		}
		gotVel := rectOffsets{rm.x.Spring.Velocity, rm.y.Spring.Velocity, rm.w.Spring.Velocity, rm.h.Spring.Velocity}
		for i, pair := range [][2]float64{
			{gotOff.x, wantAbs.x}, {gotOff.y, wantAbs.y}, {gotOff.w, wantAbs.w}, {gotOff.h, wantAbs.h},
			{gotVel.x, wantVel.x}, {gotVel.y, wantVel.y}, {gotVel.w, wantVel.w}, {gotVel.h, wantVel.h},
			{rm.df, 1 + wantFade}, {rm.fade.Spring.Velocity, wantFadeV},
		} {
			if math.Abs(pair[0]-pair[1]) > 1e-6 {
				t.Errorf("component %d: got %g want %g", i, pair[0], pair[1])
			}
		}
		if wantVel == (rectOffsets{}) || wantFadeV == 0 {
			t.Fatalf("fixture: the leaving card must still move (vel %+v, fade %g)", wantVel, wantFadeV)
		}

	})
}

func TestOverviewLeavingRetainsContentScale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewRig(t)
		sc.mon.Current().Overflow = OverflowCascade
		sc.mon.AddWindow(4)
		settleShown(t, c)
		toggleOverview(c, sc, ic.now)
		before := frame(t, c, ic, 30*time.Millisecond)
		shots := c.snapshot(ic.now)
		sc.mon.Focus(1)
		c.transition(shots, ic.now)
		after := indicatorScene(t, c)
		count := 0
		for id, rm := range sc.rects {
			if !rm.leaving {
				continue
			}
			count++
			old := sceneWindow(t, before, id)
			got := sceneWindow(t, after, id)
			if math.Abs(contentScale(old)-contentScale(got)) > 1e-9 {
				t.Errorf("card %d content scale jumped: %g -> %g", id, contentScale(old), contentScale(got))
			}
		}
		if count == 0 {
			t.Fatal("no leaving cards")
		}

	})
}
