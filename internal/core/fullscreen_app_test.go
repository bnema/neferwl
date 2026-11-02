package core

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"testing/synctest"

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
					{"satellite-different-apps", ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true}, ports.WindowMapped{PID: 7, AppID: "other", Xwayland: true}, false},
					{"satellite-same-app", ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true}, ports.WindowMapped{PID: 7, AppID: "game", Xwayland: true}, true},
					{"satellite-empty-app-id", ports.WindowMapped{PID: 7, Xwayland: true}, ports.WindowMapped{PID: 7, Xwayland: true}, false},
					{"satellite-cover-native-arrival", ports.WindowMapped{PID: 7, Xwayland: true}, ports.WindowMapped{PID: 7}, false},
					{"native-cover-satellite-arrival", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 7, Xwayland: true}, false},
					{"builtin-x11-pid", ports.WindowMapped{PID: 7, AppID: "launcher", Positioned: true}, ports.WindowMapped{PID: 7, AppID: "game", Positioned: true}, true},
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
