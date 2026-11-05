//go:build xwayland

package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statewire"
	"github.com/bnema/neferwl/internal/domain"
)

// TestXwaylandFullscreenGroup: a fullscreen launcher ("steam") starts a
// different program ("lineage-game") from its own process. In scroll and
// cascade the game opens fullscreen in its own column or band and covers;
// the launcher stays fullscreen, held off screen. Focus swaps the cover and
// closing the game returns to the launcher. Fixed keeps the overlay.
func TestXwaylandFullscreenGroup(t *testing.T) {
	for _, mode := range []string{"scroll", "cascade", "fixed"} {
		t.Run(mode, func(t *testing.T) { runXwaylandFullscreenGroup(t, mode) })
	}
}

func runXwaylandFullscreenGroup(t *testing.T, mode string) {
	path := filepath.Join(t.TempDir(), "config")
	text := "xwayland = builtin\nterminal.auto-open = off\nanimations = off\nlayout.overflow = " + mode + "\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := config.Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("config: %v %v", err, warnings)
	}
	const ow, oh = 640, 360
	e := startXwaylandConfig(t, [][2]int{{ow, oh}}, func(c *domain.Config) { *c = cfg }, path)
	p := startProbe(t, e.bin, e.display, "-lineage-controller", "start", "-lineage-direct")
	t.Cleanup(func() { t.Logf("probe: %s state: %+v", p.out.String(), readState(e.statePath)) })
	find := func(class string) (statewire.Window, bool) {
		for _, w := range readState(e.statePath).Windows {
			if w.AppID == class {
				return w, true
			}
		}
		return statewire.Window{}, false
	}
	workspace := func() statewire.Workspace {
		for _, w := range readState(e.statePath).Workspaces {
			if w.Output == "HEADLESS-1" && w.Visible {
				return w
			}
		}
		return statewire.Workspace{}
	}
	send := func(w io.Writer, line string) {
		t.Helper()
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	var launcher, game statewire.Window
	until(t, "fullscreen launcher", func() bool {
		var ok bool
		launcher, ok = find("steam")
		return ok && launcher.Fullscreen && workspace().Cover == domain.WindowID(launcher.ID)
	})
	send(p.in, "spawn")
	if mode == "fixed" {
		until(t, "game overlay", func() bool {
			var ok bool
			game, ok = find("lineage-game")
			return ok && game.Floating && !game.Fullscreen
		})
		if w := workspace(); w.Cover != domain.WindowID(launcher.ID) {
			t.Fatalf("fixed cover %d", w.Cover)
		}
		return
	}
	until(t, "game covers", func() bool {
		var ok bool
		game, ok = find("lineage-game")
		return ok && game.Fullscreen && workspace().Cover == domain.WindowID(game.ID)
	})
	launcher, _ = find("steam")
	if !launcher.Fullscreen || game.Floating {
		t.Fatalf("launcher %+v game %+v", launcher, game)
	}
	st := readState(e.statePath)
	g, _ := tileRect(st, game.ID)
	l, _ := tileRect(st, launcher.ID)
	if g.Min.X != 0 || g.Min.Y != 0 || g.Dx() != ow || g.Dy() != oh {
		t.Fatalf("game rect %v", g)
	}
	if l.Dx() != ow || l.Dy() != oh {
		t.Fatalf("held launcher not output-sized: %v", l)
	}
	if mode == "scroll" && l.Max.X > g.Min.X {
		t.Fatalf("launcher %v not left of game %v", l, g)
	}
	if mode == "cascade" && l.Max.Y > g.Min.Y {
		t.Fatalf("launcher %v not above game %v", l, g)
	}
	// An unrelated program waits hidden, not fullscreen (ADR 011).
	startProbe(t, e.bin, e.display, "-class", "other", "-stdin-sync")
	until(t, "unrelated mapped", func() bool { _, ok := find("other"); return ok })
	if o, _ := find("other"); o.Fullscreen || o.Visible {
		t.Fatalf("unrelated window: %+v", o)
	}
	// Focus the launcher with the keyboard: it covers again.
	back := "Super+Left"
	if mode == "cascade" {
		back = "Super+Up"
	}
	// The unrelated window waits hidden; the game keeps the cover.
	if w := workspace(); w.Cover != domain.WindowID(game.ID) {
		t.Fatalf("cover moved to %d", w.Cover)
	}
	if err := e.script.inject("key", back); err != nil {
		t.Fatal(err)
	}
	until(t, "launcher covers", func() bool {
		w := workspace()
		return w.Cover == domain.WindowID(launcher.ID) && w.Focused == domain.WindowID(launcher.ID)
	})
	if gw, _ := find("lineage-game"); !gw.Fullscreen {
		t.Fatalf("held game lost fullscreen: %+v", gw)
	}
	// Back to the game, then close it: the launcher covers and has the focus.
	fwd := strings.Replace(strings.Replace(back, "Left", "Right", 1), "Up", "Down", 1)
	if err := e.script.inject("key", fwd); err != nil {
		t.Fatal(err)
	}
	until(t, "game covers again", func() bool { return workspace().Cover == domain.WindowID(game.ID) })
	send(p.in, "close")
	until(t, "launcher restored", func() bool {
		_, exists := find("lineage-game")
		w := workspace()
		return !exists && w.Cover == domain.WindowID(launcher.ID) && w.Focused == domain.WindowID(launcher.ID)
	})
	launcher, _ = find("steam")
	if r, ok := tileRect(readState(e.statePath), launcher.ID); !launcher.Fullscreen || !ok || r.Min.X != 0 || r.Min.Y != 0 || r.Dx() != ow || r.Dy() != oh {
		t.Fatalf("launcher return: %+v %v", launcher, r)
	}
	// A second window of the launcher's own app opens in an ordinary
	// column, focused, not fullscreen; the launcher keeps its fullscreen.
	startProbe(t, e.bin, e.display, "-class", "steam", "-stdin-sync")
	var chat statewire.Window
	until(t, "same-app window", func() bool {
		for _, w := range readState(e.statePath).Windows {
			if w.AppID == "steam" && w.ID != launcher.ID {
				chat = w
				return w.Visible && workspace().Focused == domain.WindowID(w.ID)
			}
		}
		return false
	})
	if chat.Fullscreen || chat.Floating {
		t.Fatalf("same-app window: %+v", chat)
	}
	// Its own column (scroll) or band (cascade); a lone cascade band fills
	// the usable area, so only the state says it is not fullscreen. The
	// launcher stays the workspace's fullscreen window (today's same-app
	// policy).
	if _, ok := tileRect(readState(e.statePath), chat.ID); !ok {
		t.Fatal("same-app window not tiled")
	}

	if l, _ := find("steam"); l.ID == launcher.ID && !l.Fullscreen {
		t.Fatalf("launcher lost fullscreen: %+v", l)
	}
}
