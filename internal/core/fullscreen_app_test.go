package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestFullscreenAppArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		for _, floating := range []bool{false, true} {
			for _, arrivalFloating := range []bool{false, true} {
				for _, tc := range []struct {
					name        string
					first, next ports.WindowMapped
					same        bool
				}{
					{"pid", ports.WindowMapped{PID: 7, AppID: "launcher"}, ports.WindowMapped{PID: 7, AppID: "game"}, true},
					{"satellite-different-apps", ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true, PIDShared: true}, ports.WindowMapped{PID: 7, AppID: "other", Xwayland: true, PIDShared: true}, false},
					{"satellite-same-app", ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true, PIDShared: true}, ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true, PIDShared: true}, true},
					{"satellite-empty-app-id", ports.WindowMapped{PID: 7, Xwayland: true, PIDShared: true}, ports.WindowMapped{PID: 7, Xwayland: true, PIDShared: true}, false},
					{"satellite-cover-native-arrival", ports.WindowMapped{PID: 7, Xwayland: true, PIDShared: true}, ports.WindowMapped{PID: 7}, false},
					{"native-cover-satellite-arrival", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 7, Xwayland: true, PIDShared: true}, false},
					{"builtin-x11-pid", ports.WindowMapped{PID: 7, AppID: "launcher", Positioned: true, Xwayland: true}, ports.WindowMapped{PID: 7, AppID: "game", Positioned: true, Xwayland: true}, true},
					{"app-id", ports.WindowMapped{PID: 7, AppID: "app"}, ports.WindowMapped{PID: 8, AppID: "app"}, true},
					{"different", ports.WindowMapped{PID: 7, AppID: "launcher"}, ports.WindowMapped{PID: 8, AppID: "game"}, false},
					{"empty", ports.WindowMapped{}, ports.WindowMapped{}, false},
					{"pid-empty-app-id", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 7}, true},
					{"empty-app-id", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 8}, false},
				} {
					t.Run(fmt.Sprintf("%s/float=%v/arrival-float=%v/%s", mode, floating, arrivalFloating, tc.name), func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							c, _, _ := fadeCore(t)
							w := c.cur().mon.Current()
							w.SetOverflow(mode)
							tc.first.ID, tc.next.ID = 1, 2
							tc.first.Floating = floating
							tc.next.Floating = arrivalFloating
							c.clientEvent(context.Background(), tc.first)
							c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
							before := w.View
							c.clientEvent(context.Background(), tc.next)
							if w.Fullscreen != 1 {
								t.Fatal("fullscreen lost")
							}
							want := WindowID(1)
							if tc.same {
								want = 2
							}
							if id, _ := w.Focused(); id != want {
								t.Fatalf("focus %d, want %d", id, want)
							}
							for _, p := range w.Layout() {
								if p.ID == 2 && (p.Hidden == tc.same || tc.same && !p.Rect.Overlaps(w.Output)) {
									t.Fatalf("arrival placement: %+v", p)
								}
							}
							if !tc.same && w.View != before {
								t.Fatal("other app moved view")
							}
							if tc.same {
								switch mode {
								case OverflowFixed:
									if !w.IsFloat(2) || w.Cover() != 1 {
										t.Fatal("fixed arrival not above cover")
									}
								case OverflowCascade:
									if w.Band(w.ColumnOf(2)) != w.Band(w.ColumnOf(1))+1 || w.View == before {
										t.Fatal("cascade did not select new band")
									}
								case OverflowScroll:
									if w.ColumnOf(2) != w.ColumnOf(1)+1 || w.View == before {
										t.Fatal("scroll did not select right neighbor")
									}
								}
							}
						})
					})
				}
			}
		}
	}

}

// The scene, not just workspace placements, must draw arrivals over covers
// whose float eligibility deliberately prevents promotion.
func TestFullscreenUnadoptableCoverScene(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll} {
		for _, stash := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stash=%v", mode, stash), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, _, _ := fadeCore(t)
					w := c.cur().mon.Current()
					w.SetOverflow(mode)
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 9})
					parent := WindowID(9)
					if stash {
						parent = 0
					}
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 7, Floating: !stash, Parent: parent, Width: 400, Height: 300})
					if stash {
						w.FocusID(1)
						w.ToggleWindowStash()
						if !w.StashShown() {
							w.ToggleStashVisible()
						}
					}
					c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 7, Width: 200, Height: 100})
					settleShown(t, c)
					s := indicatorScene(t, c)
					p := sceneWindow(t, s, 2)
					if p.Hidden || !p.Focused || !p.Floating || !p.Rect.Overlaps(w.Output) {
						t.Fatalf("arrival not drawn: %+v", p)
					}
					cover := sceneWindow(t, s, 1)
					if !cover.Fullscreen || cover.Rect != w.Output {
						t.Fatalf("cover: %+v", cover)
					}
				})
			})
		}
	}
}

// Rule destinations in the background remain quiet even for matching apps.
// Visible destinations use the same policy for native and rule-selected floats.
func TestFullscreenAppRuleArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		for _, arrivalFloat := range []bool{false, true} {
			for _, ruleFloat := range []bool{false, true} {
				for _, background := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/native-float=%v/rule-float=%v/background=%v", mode, arrivalFloat, ruleFloat, background), func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							c, _, _ := fadeCore(t)
							w := c.cur().mon.Current()
							w.SetOverflow(mode)
							c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 7})
							c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
							before := w.View
							eff := ruleEffect{floating: &ruleFloat}
							if background {
								eff.number = 2
							}
							c.rules = []rule{{appID: regexp.MustCompile("^arrival$"), floating: eff.floating, number: eff.number}}
							c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 7, AppID: "arrival", Floating: arrivalFloat, Width: 100, Height: 80})
							if w.Fullscreen != 1 {
								t.Fatal("fullscreen lost")
							}
							if background {
								if id, _ := w.Focused(); id != 1 || w.View != before || w.Has(2) {
									t.Fatal("background rule disturbed cover")
								}
								_, target := c.screenOf(2)
								if target == nil || target == w || !target.Has(2) {
									t.Fatal("background rule destination lost")
								}
								s := indicatorScene(t, c)
								for _, p := range s.Windows {
									if p.ID == 2 && !p.Hidden {
										t.Fatalf("background arrival drawn: %+v", p)
									}
								}
								return
							}
							if id, _ := w.Focused(); id != 2 {
								t.Fatal("visible rule arrival not focused")
							}
							s := indicatorScene(t, c)
							p := sceneWindow(t, s, 2)
							if p.Hidden || !p.Rect.Overlaps(w.Output) {
								t.Fatalf("visible arrival not drawn: %+v", p)
							}
						})
					})
				}
			}
		}
	}
}

// A tiled window launched by another program from a fullscreen launcher
// inherits the fullscreen in its own column (scroll) or band (cascade): it is
// the cover and the launcher is held. Fixed keeps the overlay.
func TestFullscreenLauncherArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, cmds := fadeCore(t)
				w := c.cur().mon.Current()
				w.SetOverflow(mode)
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
				c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
				before := w.View
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
				if c.launcherOf(2) != 1 {
					t.Fatal("missing launcher")
				}
				if id, _ := w.Focused(); id != 2 || c.keyboardFocus() != 2 {
					t.Fatal("child did not take workspace and seat focus")
				}
				if mode == OverflowFixed {
					if w.Fullscreen != 1 || !w.IsFloat(2) || w.Cover() != 1 || len(w.Held) != 0 {
						t.Fatal("fixed overlay changed")
					}
					return
				}
				if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) || w.Cover() != 2 || w.View == before {
					t.Fatalf("cover %d held %v covering %d", w.Fullscreen, w.Held, w.Cover())
				}
				if mode == OverflowScroll && w.ColumnOf(2) != w.ColumnOf(1)+1 {
					t.Fatalf("child not right of launcher: %+v", w.Columns)
				}
				if mode == OverflowCascade && w.Band(w.ColumnOf(2)) != w.Band(w.ColumnOf(1))+1 {
					t.Fatalf("child not in the band below: %+v", w.Columns)
				}
				if len(w.Columns[w.ColumnOf(2)].Windows) != 1 || w.IsFloat(2) {
					t.Fatalf("child not alone in its column: %+v", w.Columns)
				}
				s := indicatorScene(t, c)
				child := sceneWindow(t, s, 2)
				if child.Hidden || !child.Focused || !child.Fullscreen || child.Rect != w.Output {
					t.Fatalf("child scene: %+v", child)
				}
				configs := configuresOf(cmds)[2]
				if len(configs) == 0 {
					t.Fatal("child not configured")
				}
				cfg := configs[len(configs)-1]
				if !cfg.Fullscreen || cfg.Floating || cfg.Width != w.Output.W || cfg.Height != w.Output.H {
					t.Fatalf("child not fullscreen at output size: %+v", cfg)
				}
				// Back to the launcher: it covers again, the child is held.
				w.FocusID(1)
				if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("return: cover %d held %v", w.Fullscreen, w.Held)
				}
				cover := sceneWindow(t, indicatorScene(t, c), 1)
				if !cover.Fullscreen || cover.Rect != w.Output {
					t.Fatalf("return to launcher: %+v", cover)
				}
			})
		})
	}
}

// Arrivals that do not inherit the launcher's fullscreen keep their path.
func TestFullscreenLauncherArrivalExceptions(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll} {
		for _, tc := range []struct {
			name  string
			next  ports.WindowMapped
			focus WindowID // workspace focus after the arrival
		}{
			// Same app (a chat or friends window): today's own column.
			{"same-app", ports.WindowMapped{ID: 2, PID: 20, AppID: "launcher", Proc: lineageNodes(20, 10)}, 2},
			// A floating child stays a float over the cover.
			{"floating", ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10), Floating: true, Width: 200, Height: 100}, 2},
			// Unrelated: hidden under the cover (ADR 011).
			{"unrelated", ports.WindowMapped{ID: 2, PID: 30, AppID: "other", Proc: lineageNodes(30)}, 1},
		} {
			t.Run(fmt.Sprintf("%s/%s", mode, tc.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, _, cmds := fadeCore(t)
					w := c.cur().mon.Current()
					w.SetOverflow(mode)
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
					c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
					c.clientEvent(context.Background(), tc.next)
					if w.Fullscreen != 1 || len(w.Held) != 0 {
						t.Fatalf("cover %d held %v", w.Fullscreen, w.Held)
					}
					if id, _ := w.Focused(); id != tc.focus {
						t.Fatalf("focus %d want %d", id, tc.focus)
					}
					p := sceneWindow(t, indicatorScene(t, c), 2)
					if p.Fullscreen || p.Hidden != (tc.name == "unrelated") {
						t.Fatalf("arrival scene: %+v", p)
					}
					if cfgs := configuresOf(cmds)[2]; len(cfgs) > 0 && cfgs[len(cfgs)-1].Fullscreen {
						t.Fatalf("arrival configured fullscreen: %+v", cfgs[len(cfgs)-1])
					}
				})
			})
		}
	}
}

// A launcher already held (its first child is the cover) holds its next
// child too: the game keeps the cover and the focus.
func TestFullscreenHeldLauncherArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll} {
		for _, mapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mapped-fullscreen=%v", mode, mapped), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, _, cmds := fadeCore(t)
					w := c.cur().mon.Current()
					w.SetOverflow(mode)
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
					c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
					// The child may map with fullscreen already set: held, it
					// must not take the cover nor the keyboard (placeMapped).
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, PID: 30, AppID: "tool", Proc: lineageNodes(30, 10), Fullscreen: mapped})
					// The game stays on screen: its column shifts, the view with it.
					if w.Fullscreen != 2 || w.Cover() != 2 || !slices.Equal(w.Held, []WindowID{1, 3}) {
						t.Fatalf("cover %d covering %d held %v", w.Fullscreen, w.Cover(), w.Held)
					}
					if g := sceneWindow(t, indicatorScene(t, c), 2); g.Hidden || g.Rect != w.Output {
						t.Fatalf("game moved off screen: %+v", g)
					}
					if id, _ := w.Focused(); id != 2 || c.keyboardFocus() != 2 {
						t.Fatalf("focus %d keyboard %d", id, c.keyboardFocus())
					}
					if mode == OverflowScroll && w.ColumnOf(3) != w.ColumnOf(1)+1 {
						t.Fatalf("held child not next to launcher: %+v", w.Columns)
					}
					p := sceneWindow(t, indicatorScene(t, c), 3)
					if !p.Fullscreen || p.Rect.W != w.Output.W || p.Rect.H != w.Output.H {
						t.Fatalf("held child scene: %+v", p)
					}
					if cfgs := configuresOf(cmds)[3]; len(cfgs) == 0 || !cfgs[len(cfgs)-1].Fullscreen {
						t.Fatalf("held child configures: %+v", cfgs)
					}
				})
			})
		}
	}
}

// A child mapping fullscreen already set takes the cover; the launcher
// survives held instead of being left (placeMapped's Activate).
func TestFullscreenLauncherArrivalMappedFullscreen(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, _ := fadeCore(t)
				w := c.cur().mon.Current()
				w.SetOverflow(mode)
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
				c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
				c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10), Fullscreen: true})
				if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) {
					t.Fatalf("cover %d held %v", w.Fullscreen, w.Held)
				}
				if p := sceneWindow(t, indicatorScene(t, c), 1); !p.Fullscreen {
					t.Fatalf("launcher lost fullscreen: %+v", p)
				}
			})
		})
	}
}

// On a remote keep-focus output the child advances there; the seat stays.
//
// With stash.capture on and the desktop stash shown on the focused output,
// the tv launch still goes to tv: the desktop stash is left as it was.
func TestFullscreenLauncherArrivalRemote(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run(fmt.Sprintf("desktop-stash-capture=%v", capture), func(t *testing.T) {
			runLauncherArrivalRemote(t, capture)
		})
	}
}

func runLauncherArrivalRemote(t *testing.T, capture bool) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 9, AppID: "kitty"})
		desktop := c.cur()
		dw := desktop.mon.Current()
		if capture {
			desktop.mon.SetStashCapture(true)
			c.clientEvent(context.Background(), ports.WindowMapped{ID: 8, AppID: "notes"})
			dw.FocusID(8)
			dw.ToggleWindowStash()
			if !dw.StashShown() || dw.StashIndex(8) < 0 || !desktop.mon.captures(dw) {
				t.Fatalf("setup: desktop stash %+v shown %v", dw.Stash, dw.StashShown())
			}
		}
		stash := slices.Clone(dw.Stash)
		seat, keyboard, pointer := c.focusScreen, c.keyboardFocus(), c.pointer
		view, workspace := dw.View, desktop.mon.Current()
		x, y := c.cursorX, c.cursorY
		assertSeat := func() {
			t.Helper()
			if c.focusScreen != seat || c.keyboardFocus() != keyboard || c.pointer != pointer || c.cursorX != x || c.cursorY != y || desktop.mon.Current() != workspace || dw.View != view {
				t.Fatal("remote activity changed seat or desktop view")
			}
		}
		c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
		target := c.screens[c.screenIndex("tv")]
		c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", KeepFocus: true}}
		c.rules = []rule{{appID: regexp.MustCompile("^picture$"), output: "tv"}}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, AppID: "picture", Proc: lineageNodes(10), Xwayland: true, Fullscreen: true})
		w := target.mon.Current()
		w.SetOverflow(OverflowScroll)
		if w.Fullscreen != 1 {
			t.Fatalf("launcher not fullscreen on tv: %d", w.Fullscreen)
		}
		indicatorScene(t, c)
		assertSeat()
		if c.x11.sent != 1 {
			t.Fatalf("launcher X11 selection %d", c.x11.sent)
		}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, AppID: "game", Proc: lineageNodes(20, 10), Xwayland: true})
		if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) {
			t.Fatalf("tv cover %d held %v", w.Fullscreen, w.Held)
		}
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("tv focus %d", id)
		}
		indicatorScene(t, c)
		assertSeat()
		if c.x11.sent != 2 {
			t.Fatalf("child X11 selection %d", c.x11.sent)
		}
		// A second program from the now-held launcher joins held, even if
		// it requests fullscreen. It must not promote itself or move the seat.
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, AppID: "other-game", Proc: lineageNodes(30, 10), Xwayland: true, Fullscreen: true})
		indicatorScene(t, c)
		assertSeat()
		if !w.IsHeld(3) || w.Fullscreen != 2 || c.x11.sent != 2 {
			t.Fatalf("held arrival: cover %d held %v X11 %d", w.Fullscreen, w.Held, c.x11.sent)
		}
		// Closing the active child promotes its launcher locally, not at the seat.
		c.clientEvent(context.Background(), ports.WindowUnmapped{ID: 2})
		indicatorScene(t, c)
		assertSeat()
		if w.Fullscreen != 1 || c.x11.sent != 1 {
			t.Fatalf("launcher return: cover %d X11 %d", w.Fullscreen, c.x11.sent)
		}
		if dw.Has(2) || !slices.Equal(dw.Stash, stash) || dw.StashShown() != capture {
			t.Fatalf("desktop stash changed: %+v (was %+v) shown %v", dw.Stash, stash, dw.StashShown())
		}
	})
}

// A launcher cover the layout cannot adopt (a dialog float or a stashed
// window) stays a float, held and hidden, while the program it launched
// covers from a column or band of its own. Returning to the launcher shows
// it fullscreen again as a floating cover. Fixed keeps the overlay.
func TestFullscreenUnadoptableLauncherArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		for _, stash := range []bool{false, true} {
			for _, back := range []string{"activate", "focus"} {
				t.Run(fmt.Sprintf("%s/stash=%v/%s", mode, stash, back), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						c, _, cmds := fadeCore(t)
						w := c.cur().mon.Current()
						w.SetOverflow(mode)
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 9})
						parent := WindowID(9)
						if stash {
							parent = 0
						}
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10), Floating: !stash, Parent: parent, Width: 400, Height: 300})
						if stash {
							w.FocusID(1)
							w.ToggleWindowStash()
							if !w.StashShown() {
								w.ToggleStashVisible()
							}
						}
						c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
						if w.Fullscreen != 1 {
							t.Fatalf("launcher not fullscreen: %d", w.Fullscreen)
						}
						// A frame configures the launcher fullscreen first.
						settleShown(t, c)
						indicatorScene(t, c)
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
						if mode == OverflowFixed {
							if w.Fullscreen != 1 || len(w.Held) != 0 || !w.IsFloat(2) {
								t.Fatalf("fixed changed: cover %d held %v", w.Fullscreen, w.Held)
							}
							return
						}
						if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) || !w.IsFloat(1) || w.ColumnOf(2) < 0 {
							t.Fatalf("cover %d held %v float %v col %d", w.Fullscreen, w.Held, w.IsFloat(1), w.ColumnOf(2))
						}
						if id, _ := w.Focused(); id != 2 || c.keyboardFocus() != 2 {
							t.Fatalf("focus %d", id)
						}
						settleShown(t, c)
						s := indicatorScene(t, c)
						if g := sceneWindow(t, s, 2); g.Hidden || !g.Fullscreen || g.Rect != w.Output {
							t.Fatalf("game scene: %+v", g)
						}
						if l := sceneWindow(t, s, 1); !l.Hidden {
							t.Fatalf("held launcher shown: %+v", l)
						}
						if cfgs := configuresOf(cmds)[1]; len(cfgs) == 0 || !cfgs[len(cfgs)-1].Fullscreen {
							t.Fatalf("held launcher configures: %+v", cfgs)
						}
						// Back to the launcher: a floating cover again.
						if back == "activate" {
							w.Activate(1)
						} else {
							w.FocusID(1)
						}
						if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) || !w.IsFloat(1) || w.Cover() != 1 {
							t.Fatalf("return: cover %d held %v covering %d", w.Fullscreen, w.Held, w.Cover())
						}
						settleShown(t, c)
						s = indicatorScene(t, c)
						if l := sceneWindow(t, s, 1); l.Hidden || !l.Fullscreen || l.Rect != w.Output {
							t.Fatalf("launcher scene: %+v", l)
						}
						if g := sceneWindow(t, s, 2); !g.Hidden {
							t.Fatalf("held game shown: %+v", g)
						}
					})
				})
			}
		}
	}
}
