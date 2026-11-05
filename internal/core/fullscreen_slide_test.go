package core

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// drawn lists the windows the scene draws on its output.
func drawn(s domain.Scene) []domain.SceneWindow {
	var out []domain.SceneWindow
	for _, w := range s.Windows {
		if s.Draws(w) {
			out = append(out, w)
		}
	}
	return out
}

// scanoutCandidate is the window direct scanout could take: the only drawn
// window, fullscreen and covering the output exactly (drm's covers rule).
func scanoutCandidate(s domain.Scene) WindowID {
	d := drawn(s)
	if len(d) != 1 || !d[0].Fullscreen || d[0].Rect != (Rect{W: s.OutputWidth, H: s.OutputHeight}) {
		return 0
	}
	return d[0].ID
}

// Switching the cover between members slides the held one in instead of
// popping it: settled, only the cover is drawn; mid-slide both are, partly.
func TestFullscreenGroupSlide(t *testing.T) {
	for _, mode := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, ic, cmds := fadeCore(t)
				c.cfg.Animations.On = true
				w := c.cur().mon.Current()
				w.SetOverflow(mode)
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
				c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
				settle := func() domain.Scene {
					ic.now = ic.now.Add(10 * time.Second)
					c.animate(ic.now, nil)
					return indicatorScene(t, c)
				}
				s := settle()
				if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) {
					t.Fatalf("setup: cover %d held %v", w.Fullscreen, w.Held)
				}
				if got := scanoutCandidate(s); got != 2 {
					t.Fatalf("settled candidate %d, drawn %+v", got, drawn(s))
				}
				if p := sceneWindow(t, s, 1); p.Hidden || !p.Fullscreen {
					t.Fatalf("held tile hidden instead of off screen: %+v", p)
				}
				// Back to the launcher with the keyboard.
				a := ActionFocusColumnLeft
				if mode == OverflowCascade {
					a = ActionFocusWindowUp
				}
				shots := c.snapshot(ic.now)
				c.applyAction(a)
				c.transition(shots, ic.now)
				if w.Fullscreen != 1 {
					t.Fatalf("no promotion: cover %d", w.Fullscreen)
				}
				ic.now = ic.now.Add(30 * time.Millisecond)
				c.animate(ic.now, nil)
				mid := indicatorScene(t, c)
				d := drawn(mid)
				ids := []WindowID{}
				for _, p := range d {
					ids = append(ids, p.ID)
					if p.Rect == (Rect{W: mid.OutputWidth, H: mid.OutputHeight}) {
						t.Fatalf("mid-slide window %d already settled: %+v", p.ID, p)
					}
				}
				slices.Sort(ids)
				if !slices.Equal(ids, []WindowID{1, 2}) || scanoutCandidate(mid) != 0 {
					t.Fatalf("mid-slide drawn %v candidate %d", ids, scanoutCandidate(mid))
				}
				s = settle()
				if got := scanoutCandidate(s); got != 1 {
					t.Fatalf("after slide candidate %d, drawn %+v", got, drawn(s))
				}
				// Held tiles off screen are told they are not visible, like
				// any scrolled-off tile, and stay fullscreen.
				cfgs := configuresOf(cmds)[2]
				if len(cfgs) == 0 {
					t.Fatal("game not configured")
				}
				if last := cfgs[len(cfgs)-1]; last.Visible || last.Activated || !last.Fullscreen || last.Width != w.Output.W || last.Height != w.Output.H {
					t.Fatalf("held game configure: %+v", last)
				}
			})
		})
	}
}

// Unrelated windows stay hidden under the cover; a held float stays hidden.
func TestFullscreenGroupCoverHidesOthers(t *testing.T) {
	for _, mode := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, _ := fadeCore(t)
				w := c.cur().mon.Current()
				w.SetOverflow(mode)
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
				c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, PID: 30, AppID: "other", Proc: lineageNodes(30)})
				w.AddFloating(4, 100, 50)
				w.Held = append(w.Held, 4)
				s := indicatorScene(t, c)
				for _, id := range []WindowID{3, 4} {
					if p := sceneWindow(t, s, id); !p.Hidden {
						t.Fatalf("window %d shown under the cover: %+v", id, p)
					}
				}
			})
		})
	}
}
