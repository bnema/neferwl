package core_test

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
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
		h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: ports.ModSuper}
		synctest.Wait()
		fire <- time.Unix(0, int64(400*time.Millisecond))
		synctest.Wait()
		o := osdOverlay(t, h)
		if o.Kind != ports.OverlayPanel || o.Anchor != ports.AnchorTopCenter || o.Capture || o.Panel == nil {
			t.Fatalf("overlay: %+v", o)
		}
		if !strings.Contains(o.Panel.Lines[0], "org.test.app") {
			t.Fatal(o.Panel.Lines)
		}
		next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 1 })
		h.client <- ports.WindowStats{Window: 1, ClientFPS: 144, ShownFPS: 120, ScanoutReason: "overlays"}
		synctest.Wait()
		updated := osdOverlay(t, h)
		if updated.Seq <= o.Seq || !strings.Contains(updated.Panel.Lines[2], "144 fps") || !strings.Contains(updated.Panel.Lines[1], "off (overlay)") {
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
			h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: ports.ModSuper}
			synctest.Wait()
			h.input <- ports.KeyEvent{Keysym: "Super_L"}
			synctest.Wait()
		}
		tap()
		tap()
		osdOverlay(t, h)
		h.client <- ports.WindowMapped{ID: 2, AppID: "second"}
		synctest.Wait()
		osdOverlay(t, h)
		next(t, h.commands, func(v ports.WatchStats) bool { return v.Window == 2 })
		tap()
		tap()
		s := <-h.scenes
		if len(s[0].Overlays) != 0 {
			t.Fatal("unpin retained overlay")
		}
		h.input <- ports.KeyEvent{Keysym: "p", Base: "p", Pressed: true, Mods: ports.ModSuper}
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
				h.input <- ports.KeyEvent{Keysym: "Super_L", Pressed: true, Mods: ports.ModSuper}
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
