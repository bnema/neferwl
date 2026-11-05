package core

import (
	"context"
	"regexp"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPlacementFocusDomain(t *testing.T) {
	for _, tc := range []struct {
		name          string
		keep, current bool
	}{
		{"remote keep-focus", true, false},
		{"remote ordinary", false, false},
		{"current keep-focus", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, cmds := fadeCore(t)
				desktop := c.cur()
				desktop.info.Name = "DP-2"
				c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
				tv := c.screens[c.screenIndex("tv")]
				c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", Virtual: true, KeepFocus: tc.keep}}
				c.focusScreen = c.screenIndex("DP-2")
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, AppID: "kitty"})
				c.pointer = 1
				c.cursorX, c.cursorY = 40, 50
				beforeView := desktop.mon.Current().View
				c.rules = []rule{{appID: regexp.MustCompile("^picture$"), output: "tv"}}
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, AppID: "picture", Xwayland: true, Fullscreen: true, Proc: lineageNodes(20)})
				indicatorScene(t, c)
				if tc.keep {
					if c.cur() != desktop || c.keyboardFocus() != 1 || c.pointer != 1 {
						t.Fatal("rule stole seat focus")
					}
					if f, _ := tv.mon.Focused(); f != 2 || c.x11.sent != 2 {
						t.Fatal("rule did not advance kept workspace/X11 focus")
					}
				}
				// Put the seat back on desktop for the ordinary-output quiet case, or
				// explicitly visit tv to exercise current-output placement.
				c.focusScreen = c.screenIndex("DP-2")
				if !tc.keep {
					desktop.mon.Current().SetFullscreen(1, true)
				}
				if tc.current {
					c.focusScreen = c.screenIndex("tv")
				}
				screenBefore := c.focusScreen
				keyboardBefore := c.keyboardFocus()
				pointerBefore := c.pointer
				x, y := c.cursorX, c.cursorY
				w := tv.mon.Current()
				before := w.View
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, AppID: "game", Xwayland: true, Proc: lineageNodes(30, 20)})
				indicatorScene(t, c)
				if tc.keep && !tc.current && (c.focusScreen != screenBefore || c.keyboardFocus() != keyboardBefore || c.pointer != pointerBefore || c.cursorX != x || c.cursorY != y || desktop.mon.Current().View != beforeView) {
					t.Fatal("remote arrival changed seat domain")
				}
				if tc.keep || tc.current {
					if len(w.Columns) != 2 || len(w.Columns[0].Windows) != 1 || len(w.Columns[1].Windows) != 1 || w.ColumnOf(2) != 0 || w.ColumnOf(3) != 1 || w.View == before {
						t.Fatalf("not separate selected strips: %+v", w.Columns)
					}
					if f, _ := w.Focused(); f != 3 || c.x11.sent != 3 {
						t.Fatal("child did not take workspace/X11 focus")
					}
					cfgs := configuresOf(cmds)[3]
					if len(cfgs) == 0 {
						t.Fatal("missing configure")
					}
					cfg := cfgs[len(cfgs)-1]
					if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > w.Output.W || cfg.Height > w.Output.H {
						t.Fatalf("configure: %+v", cfg)
					}
					if tc.current && c.keyboardFocus() != 3 {
						t.Fatal("current output did not follow child")
					}
				} else {
					if f, _ := w.Focused(); f != 2 || w.View != before {
						t.Fatal("ordinary remote workspace no longer quiet")
					}
				}
			})
		})
	}
}

func TestTitleRuleKeepFocusDoesNotMoveSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := fadeCore(t)
		desktop := c.cur()
		c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
		c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", Virtual: true, KeepFocus: true}}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, AppID: "steam", Title: "desktop", Xwayland: true})
		c.rules = []rule{{appID: regexp.MustCompile("^steam$"), title: regexp.MustCompile("picture"), output: "tv", key: "picture"}}
		c.clientEvent(context.Background(), ports.WindowTitle{ID: 1, Title: "picture"})
		sc, _ := c.screenOf(1)
		if sc.name() != "tv" || c.cur() != desktop {
			t.Fatal("title move changed seat output or lost destination")
		}
	})
}
