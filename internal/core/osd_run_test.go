package core_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func osdOverlay(t *testing.T, h *hintHarness) ports.Overlay {
	t.Helper()
	s := <-h.scenes
	for _, o := range s[0].Overlays {
		if o.ID == 1<<61 {
			return o
		}
	}
	t.Fatal("missing OSD")
	return ports.Overlay{}
}

func TestOSDHoldThroughRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := portsmocks.NewMockClock(t)
		clock.EXPECT().Now().Return(time.Unix(0, 0))
		timer := portsmocks.NewMockTimer(t)
		fire := make(chan time.Time, 1)
		clock.EXPECT().NewTimer(400 * time.Millisecond).Return(timer).Once()
		timer.EXPECT().C().Return(fire).Once()
		timer.EXPECT().Stop().Return(true).Once()
		h := newHint(t, core.Options{Clock: clock})
		h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: domain.ModSuper}
		synctest.Wait()
		fire <- time.Unix(0, int64(400*time.Millisecond))
		synctest.Wait()
		o := osdOverlay(t, h)
		if o.Window != 1 || o.Rect != (domain.Rect{Y: 12}) || o.Kind != domain.OverlayPanel || o.Anchor != domain.AnchorTopCenter || o.Capture || o.Panel == nil {
			t.Fatalf("overlay: %+v", o)
		}
		if !strings.Contains(o.Panel.Lines[0], "org.test.app") {
			t.Fatal(o.Panel.Lines)
		}
		next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 1 })
		h.client <- ports.WindowStats{Window: 1, ClientFPS: 144, ShownFPS: 120, ScanoutReason: "overlays"}
		synctest.Wait()
		updated := osdOverlay(t, h)
		if updated.Seq <= o.Seq || !strings.Contains(updated.Panel.Lines[2], "144 fps") || !strings.Contains(updated.Panel.Lines[1], "on (OSD hidden)") {
			t.Fatal(updated)
		}
		h.client <- ports.WindowStats{Window: 2, ClientFPS: 999}
		synctest.Wait()
		select {
		case <-h.scenes:
			t.Fatal("unwatched stats published")
		default:
		}
		h.input <- ports.KeyEvent{Keysym: "Super_L"}
		synctest.Wait()
		s := <-h.scenes
		if len(s[0].Overlays) != 0 {
			t.Fatal(s[0].Overlays)
		}
		next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 0 })
	})
}

func TestOSDPinThroughRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHintConfig(t, core.Options{}, func(cfg *ports.Config) { cfg.Binds["Cmd+p"] = "osd-pin" })
		tap := func() {
			h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: domain.ModSuper}
			synctest.Wait()
			h.input <- ports.KeyEvent{Keysym: "Super_L"}
			synctest.Wait()
		}
		tap()
		tap()
		osdOverlay(t, h)
		h.client <- ports.WindowMapped{ID: 2, AppID: "second"}
		synctest.Wait()
		if o := osdOverlay(t, h); o.Window != 1 {
			t.Fatal(o)
		}
		for len(h.commands) > 0 {
			if v, ok := (<-h.commands).(ports.WatchStats); ok && v.Window != 1 {
				t.Fatalf("focus retargeted: %+v", v)
			}
		}
		tap()
		tap()
		s := <-h.scenes
		if len(s[0].Overlays) != 0 {
			t.Fatal("unpin retained overlay")
		}
		h.input <- ports.KeyEvent{Keysym: "p", Base: "p", Pressed: true, Mods: domain.ModSuper}
		synctest.Wait()
		osdOverlay(t, h)
	})
}

func TestOSDSuppressedThroughRun(t *testing.T) {
	for _, inhibited := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "inhibited"}[inhibited], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHintConfig(t, core.Options{}, func(cfg *ports.Config) { cfg.OSD.Enable = inhibited })
				if inhibited {
					h.client <- ports.ShortcutsInhibit{Window: 1, Active: true}
					synctest.Wait()
					h.drain()
				}
				h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: domain.ModSuper}
				synctest.Wait()
				h.input <- ports.KeyEvent{Keysym: "Super_L"}
				synctest.Wait()
				forwarded := 0
				for len(h.commands) > 0 {
					switch v := (<-h.commands).(type) {
					case ports.WatchStats:
						t.Fatalf("unexpected watch: %+v", v)
					case ports.ForwardKey:
						if v.ID == 1 {
							forwarded++
						}
					}
				}
				if forwarded != 2 {
					t.Fatalf("forwarded %d keys", forwarded)
				}
				select {
				case s := <-h.scenes:
					if len(s[0].Overlays) != 0 {
						t.Fatal(s[0].Overlays)
					}
				default:
				}
			})
		})
	}
}

func TestOSDPinnedSecurityTransitionThroughRun(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(map[bool]string{true: "lock", false: "epoch"}[protected], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var epoch atomic.Value
				epoch.Store(ports.SecurityState{})
				gate := portsmocks.NewMockSessionSecurity(t)
				gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
				h := newHintConfig(t, core.Options{Security: gate}, func(cfg *ports.Config) { cfg.Binds["Cmd+p"] = "osd-pin" })
				h.press("p", domain.ModSuper)
				h.release("p", 0)
				synctest.Wait()
				osdOverlay(t, h)
				next(t, h.commands, func(v ports.SecurityCommand) bool {
					watch, ok := v.Command.(ports.WatchStats)
					return ok && watch.Window == 1
				})
				state := ports.SecurityState{Generation: 1, Protected: protected}
				epoch.Store(state)
				h.client <- ports.SessionLockChanged{State: state}
				synctest.Wait()
				for _, s := range <-h.scenes {
					for _, o := range s.Overlays {
						if o.ID == 1<<61 {
							t.Fatal("security transition retained OSD")
						}
					}
				}
				next(t, h.commands, func(v ports.SecurityCommand) bool {
					watch, ok := v.Command.(ports.WatchStats)
					return v.State == state && ok && watch.Window == 0
				})
				// A fresh pin must show, not toggle an old retained pin off.
				h.state = ports.SecurityState{Generation: 2}
				epoch.Store(h.state)
				h.client <- ports.SessionLockChanged{State: h.state}
				synctest.Wait()
				h.press("p", domain.ModSuper)
				h.release("p", 0)
				synctest.Wait()
				osdOverlay(t, h)
			})
		})
	}
}

func TestOSDPinnedEmptyFocusThroughRun(t *testing.T) {
	for _, unmap := range []bool{true, false} {
		t.Run(map[bool]string{true: "unmap", false: "empty-workspace"}[unmap], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHintConfig(t, core.Options{}, func(cfg *ports.Config) {
					cfg.Binds["Cmd+p"] = "osd-pin"
					cfg.Binds["Cmd+n"] = "focus-workspace-next"
					cfg.Binds["Cmd+b"] = "focus-workspace-prev"
				})
				h.press("p", domain.ModSuper)
				h.release("p", 0)
				synctest.Wait()
				osdOverlay(t, h)
				next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 1 })
				h.client <- ports.WindowStats{Window: 1, ClientFPS: 144}
				synctest.Wait()
				old := osdOverlay(t, h)
				if !strings.Contains(old.Panel.Lines[2], "144 fps") {
					t.Fatal(old.Panel)
				}
				if unmap {
					h.client <- ports.WindowUnmapped{ID: 1}
				} else {
					h.press("n", domain.ModSuper)
					h.release("n", 0)
				}
				synctest.Wait()
				if unmap {
					for _, o := range (<-h.scenes)[0].Overlays {
						if o.ID == 1<<61 {
							t.Fatal("unmap retained panel")
						}
					}
					next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 0 })
				} else {
					// The target's workspace is hidden: the panel is not
					// published (it would refuse scanout for nothing), but
					// the pin, target and watch stay.
					for _, o := range (<-h.scenes)[0].Overlays {
						if o.ID == 1<<61 {
							t.Fatal("panel published for a window not drawn", o)
						}
					}
					for len(h.commands) > 0 {
						if v, ok := (<-h.commands).(ports.WatchStats); ok {
							t.Fatalf("unexpected watch: %+v", v)
						}
					}
					h.press("b", domain.ModSuper)
					h.release("b", 0)
					synctest.Wait()
					for len(h.scenes) > 1 {
						<-h.scenes
					}
					o := osdOverlay(t, h)
					if o.Window != 1 || o.Panel != old.Panel {
						t.Fatal("empty focus retargeted", o)
					}
				}
			})
		})
	}
}

func TestOSDCmdBindReleaseThroughRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHintConfig(t, core.Options{}, func(cfg *ports.Config) { cfg.Binds["Cmd+h"] = "close-window" })
		for _, ev := range []ports.KeyEvent{
			{Keysym: "Super_L", Pressed: true, Mods: domain.ModSuper},
			{Keysym: "h", Base: "h", Pressed: true, Mods: domain.ModSuper},
			{Keysym: "Super_L"},
			{Keysym: "h", Base: "h"},
		} {
			h.input <- ev
			synctest.Wait()
			select {
			case scenes := <-h.scenes:
				for _, s := range scenes {
					for _, o := range s.Overlays {
						if o.ID == 1<<61 {
							t.Fatal("chord showed OSD")
						}
					}
				}
			default:
			}
		}
		next(t, h.commands, func(v ports.CloseWindow) bool { return v.ID == 1 })
		for len(h.commands) > 0 {
			if v, ok := (<-h.commands).(ports.WatchStats); ok {
				t.Fatalf("chord watched stats: %+v", v)
			}
		}
	})
}

func TestOSDInhibitShownThroughRun(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "pinned"}[pinned], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := portsmocks.NewMockClock(t)
				clock.EXPECT().Now().Return(time.Unix(0, 0))
				timer := portsmocks.NewMockTimer(t)
				fire := make(chan time.Time, 1)
				clock.EXPECT().NewTimer(400 * time.Millisecond).Return(timer).Once()
				timer.EXPECT().C().Return(fire).Once()
				timer.EXPECT().Stop().Return(true).Once()
				h := newHintConfig(t, core.Options{Clock: clock}, func(cfg *ports.Config) { cfg.Binds["Cmd+p"] = "osd-pin" })
				h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: domain.ModSuper}
				synctest.Wait()
				fire <- time.Unix(0, int64(400*time.Millisecond))
				synctest.Wait()
				osdOverlay(t, h)
				next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 1 })
				if pinned {
					h.press("p", domain.ModSuper)
					synctest.Wait()
					osdOverlay(t, h)
				}
				h.client <- ports.ShortcutsInhibit{Window: 1, Active: true}
				synctest.Wait()
				if !pinned {
					if s := <-h.scenes; len(s[0].Overlays) != 0 {
						t.Fatal("inhibition retained transient overlay")
					}
					next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 0 })
				}
				h.input <- ports.KeyEvent{Keysym: "Super_L"}
				synctest.Wait()
				if pinned {
					h.client <- ports.WindowStats{Window: 1, ClientFPS: 144}
					synctest.Wait()
					osdOverlay(t, h)
				} else {
					h.client <- ports.WindowStats{Window: 1, ClientFPS: 144}
					synctest.Wait()
					select {
					case <-h.scenes:
						t.Fatal("release or stale stats republished overlay")
					default:
					}
				}
			})
		})
	}
}
