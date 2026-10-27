package core_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

type hintHarness struct {
	t        *testing.T
	input    chan ports.InputEvent
	client   chan ports.ClientEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	scenes   chan []domain.Scene
	scans    chan domain.HintScanRequest
	hints    chan domain.HintScanResult
	win      domain.Rect // window 1, output-local
	// state is the security state input is stamped with.
	state  domain.SecurityState
	config chan domain.ConfigChanged
	cfg    domain.Config
}

// newHint starts a core with one 1000x800 output "OUT-1" and window 1 mapped.
func newHint(t *testing.T, opts core.Options) *hintHarness {
	t.Helper()
	return newHintConfig(t, opts, func(*domain.Config) {})
}

// newHintConfig is newHint with the config edited by edit.
func newHintConfig(t *testing.T, opts core.Options, edit func(*domain.Config)) *hintHarness {
	t.Helper()
	cfg := scrollDefaults()
	edit(&cfg)
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 20
	cfg.Binds["Cmd+g"] = "hint"
	h := &hintHarness{
		config:   make(chan domain.ConfigChanged),
		cfg:      cfg,
		t:        t,
		input:    make(chan ports.InputEvent, 32),
		client:   make(chan ports.ClientEvent, 32),
		output:   make(chan ports.OutputEvent, 8),
		commands: make(chan ports.ClientCommand, 256),
		scenes:   make(chan []domain.Scene, 1),
		scans:    make(chan domain.HintScanRequest, 1),
		// Unbuffered: a send returns once core took the result.
		hints: make(chan domain.HintScanResult),
	}
	ch := core.Channels{Config: h.config, Client: h.client, Input: h.input, Output: h.output, Commands: h.commands, Scenes: h.scenes, HintScans: h.scans, Hints: h.hints}
	c, err := core.New(cfg, ch, testOptions(t, opts))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); receive(t, done) })
	h.output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
	scene(t, h.scenes)
	h.client <- ports.WindowMapped{ID: 1, AppID: "org.test.app", PID: 4242}
	s := scene(t, h.scenes)
	h.win = s.Windows[0].Rect
	h.drain()
	return h
}

func (h *hintHarness) drain() {
	for {
		select {
		case <-h.commands:
		default:
			return
		}
	}
}

// press and release send one key event.
func (h *hintHarness) press(sym string, mods domain.Mods) {
	h.send(ports.KeyEvent{Keysym: sym, Base: sym, Mods: mods, Pressed: true})
}

func (h *hintHarness) release(sym string, mods domain.Mods) {
	h.send(ports.KeyEvent{Keysym: sym, Base: sym, Mods: mods})
}

// send gives core an input event stamped with the current security state.
func (h *hintHarness) send(ev ports.InputEvent) {
	h.input <- ports.SecurityInput{State: h.state, Event: ev}
}

// start presses the hint bind and returns the request.
func (h *hintHarness) start() domain.HintScanRequest {
	h.t.Helper()
	h.press("g", domain.ModSuper)
	req := receive(h.t, h.scans)
	h.release("g", 0) // the user lets go of the whole chord
	return req
}

// show starts hint mode and answers with targets, returning the next scene.
func (h *hintHarness) show(targets ...domain.HintTarget) domain.Scene {
	h.t.Helper()
	req := h.start()
	h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: targets}
	return h.labelled(len(targets), 0)
}

// until returns the first scene, from the next one on, that satisfies ok:
// the bind press and other input publish scenes before the one waited for.
func (h *hintHarness) until(ok func(domain.Scene) bool) domain.Scene {
	h.t.Helper()
	for {
		if s := scene(h.t, h.scenes); ok(s) {
			return s
		}
	}
}

// labelled waits for a scene with n labels, all typed with typed letters.
func (h *hintHarness) labelled(n, typed int) domain.Scene {
	h.t.Helper()
	return h.until(func(s domain.Scene) bool {
		return len(s.Overlays) == n && (n == 0 || s.Overlays[0].Typed == typed)
	})
}

// inside is a target of the given size inside window 1.
func (h *hintHarness) inside(i int) domain.HintTarget {
	return domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + 10 + 100*(i%4), Y: h.win.Y + 10 + 40*(i/4), W: 40, H: 10}}
}

func TestHintRequestOnBind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		if req.Output != "OUT-1" || req.Width != 1000 || req.Height != 800 || req.Scale != 1 || req.Seq == 0 {
			t.Fatalf("request %+v", req)
		}
		// A second bind while pending does not ask again; the pending phase
		// cancels on any key.
		h.press("g", domain.ModSuper)
		synctest.Wait()
		select {
		case r := <-h.scans:
			t.Fatalf("second request %+v", r)
		default:
		}

	})
}

func TestHintRequestNilScansIsNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := scrollDefaults()
		cfg.Binds["Cmd+g"] = "hint"
		input := make(chan ports.InputEvent, 4)
		client := make(chan ports.ClientEvent, 4)
		output := make(chan ports.OutputEvent, 4)
		commands := make(chan ports.ClientCommand, 32)
		scenes := make(chan []domain.Scene, 1)
		c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, testOptions(t, core.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() { cancel(); receive(t, done) }()
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
		scene(t, scenes)
		input <- ports.KeyEvent{Keysym: "g", Base: "g", Mods: domain.ModSuper, Pressed: true}
		if s := scene(t, scenes); len(s.Overlays) != 0 {
			t.Fatal(s.Overlays)
		}

	})
}

func TestHintRefusedWhenProtected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var state atomic.Pointer[domain.SecurityState]
		state.Store(&domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return *state.Load() }).Maybe()
		h := newHint(t, core.Options{Security: gate})
		// Hint mode shown, then the session locks: the labels go away.
		h.show(h.inside(0))
		state.Store(&domain.SecurityState{Generation: 2, Protected: true})
		h.state = *state.Load()
		h.client <- ports.WindowMapped{ID: 2}
		s := h.until(func(s domain.Scene) bool { return s.Security.Protected })
		if len(s.Overlays) != 0 {
			t.Fatalf("labels while protected %+v", s.Overlays)
		}
		// And a new request is refused while it lasts.
		h.press("g", domain.ModSuper)
		synctest.Wait()
		select {
		case r := <-h.scans:
			t.Fatalf("request while protected %+v", r)
		default:
		}

	})
}

func TestHintResultShowsLabels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		var targets []domain.HintTarget
		for i := 0; i < 26; i++ {
			targets = append(targets, h.inside(i))
		}
		s := h.show(targets...)
		if len(s.Overlays) != 26 {
			t.Fatalf("%d labels", len(s.Overlays))
		}
		seen := map[string]bool{}
		for _, l := range s.Overlays {
			if len(l.Text) != 1 || l.Typed != 0 || seen[l.Text] {
				t.Fatalf("label %+v", l)
			}
			seen[l.Text] = true
		}
		// Sorted by position: the first target gets the first letter.
		if s.Overlays[0].Text != "f" || s.Overlays[0].Rect.X != targets[0].Rect.X || s.Overlays[0].Rect.Y != targets[0].Rect.Y {
			t.Fatalf("first %+v", s.Overlays[0])
		}

	})
}

func TestHintTwoLetterLabels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		var targets []domain.HintTarget
		for i := 0; i < 27; i++ {
			targets = append(targets, domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + 5 + 100*(i%4), Y: h.win.Y + 5 + 40*(i/4), W: 30, H: 10}})
		}
		s := h.show(targets...)
		if len(s.Overlays) != 27 {
			t.Fatalf("%d labels", len(s.Overlays))
		}
		seen := map[string]bool{}
		for _, l := range s.Overlays {
			if len(l.Text) != 2 || seen[l.Text] {
				t.Fatalf("label %+v", l)
			}
			seen[l.Text] = true
		}
		if s.Overlays[0].Text != "ff" || s.Overlays[1].Text != "fj" {
			t.Fatalf("labels %v %v", s.Overlays[0].Text, s.Overlays[1].Text)
		}

	})
}

func TestHintDropsTargetsOutsideWindows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		in := h.inside(0)
		// In the gap around the window, over the background.
		out := domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: 2, Y: 2, W: 4, H: 4}}
		if h.win.X < 8 || h.win.Y < 8 {
			t.Fatalf("window %+v leaves no gap", h.win)
		}
		req := h.start()
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{out, in}}
		s := h.labelled(1, 0)
		if len(s.Overlays) != 1 || s.Overlays[0].Rect.X != in.Rect.X {
			t.Fatalf("labels %+v (window %+v)", s.Overlays, h.win)
		}

	})
}

func TestHintTypingFilters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		var targets []domain.HintTarget
		for i := 0; i < 27; i++ {
			targets = append(targets, domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + 5 + 100*(i%4), Y: h.win.Y + 5 + 40*(i/4), W: 30, H: 10}})
		}
		h.show(targets...)
		h.press("f", 0)
		s := h.labelled(26, 1)
		if len(s.Overlays) != 26 {
			t.Fatalf("%d labels after f", len(s.Overlays))
		}
		for _, l := range s.Overlays {
			if l.Typed != 1 || l.Text[0] != 'f' {
				t.Fatalf("label %+v", l)
			}
		}
		h.press("BackSpace", 0)
		h.labelled(27, 0)
		// Only the 27th target starts with j: its label is "jf".
		h.press("j", 0)
		s = h.labelled(1, 1)
		if len(s.Overlays) != 1 || s.Overlays[0].Text != "jf" {
			t.Fatalf("after j %+v", s.Overlays)
		}

	})
}

func TestHintClick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		tg := h.inside(0)
		h.show(tg)
		h.drain()
		h.press("f", 0)
		s := h.labelled(0, 0)
		if len(s.Overlays) != 0 {
			t.Fatalf("labels after click %+v", s.Overlays)
		}
		cx := float64(tg.Rect.X) + float64(tg.Rect.W)/2 - float64(h.win.X)
		cy := float64(tg.Rect.Y) + float64(tg.Rect.H)/2 - float64(h.win.Y)
		var gotMotion, gotPress, gotRelease, gotFocus bool
		for len(h.commands) > 0 || !gotRelease {
			var cmd ports.ClientCommand
			synctest.Wait()
			select {
			case cmd = <-h.commands:
			default:
				t.Fatalf("missing commands: focus %v motion %v press %v release %v", gotFocus, gotMotion, gotPress, gotRelease)
			}
			switch v := cmd.(type) {
			case ports.PointerFocus:
				if v.ID == 1 && v.X == cx && v.Y == cy {
					gotFocus = true
				}
			case ports.PointerMotionTo:
				if v.ID == 1 && v.X == cx && v.Y == cy {
					gotMotion = true
				}
			case ports.PointerButtonTo:
				if v.ID != 1 || v.Button != 0x110 {
					t.Fatalf("button %+v", v)
				}
				if v.Pressed {
					gotPress = true
				} else {
					gotRelease = gotPress
				}
			}
		}
		if !gotFocus || !gotMotion {
			t.Fatalf("focus %v motion %v", gotFocus, gotMotion)
		}

	})
}

func TestHintEscapeCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		h.drain()
		h.press("Escape", 0)
		s := h.labelled(0, 0)
		if len(s.Overlays) != 0 {
			t.Fatalf("labels %+v", s.Overlays)
		}
		h.release("Escape", 0)
		// A later key goes to the window again: the Escape release did not.
		h.press("x", 0)
		cmd := command(t, h.commands)
		if v, ok := cmd.(ports.ForwardKey); !ok || v.Key.Keysym != "x" {
			t.Fatalf("command %#v", cmd)
		}
		for len(h.commands) > 0 {
			if _, ok := (<-h.commands).(ports.PointerButtonTo); ok {
				t.Fatal("button sent")
			}
		}

	})
}

func TestHintKeysNotForwarded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0), h.inside(1))
		h.drain()
		// A letter that matches nothing is swallowed and does not cancel.
		h.press("q", 0)
		h.release("q", 0)
		// Any other key cancels and is swallowed.
		h.press("Return", 0)
		s := h.labelled(0, 0)
		if len(s.Overlays) != 0 {
			t.Fatalf("labels %+v", s.Overlays)
		}
		h.release("Return", 0)
		h.press("x", 0)
		cmd := command(t, h.commands)
		if v, ok := cmd.(ports.ForwardKey); !ok || v.Key.Keysym != "x" {
			t.Fatalf("first forwarded command %#v", cmd)
		}

	})
}

func TestHintStaleSeqIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		h.hints <- domain.HintScanResult{Seq: req.Seq + 7, Output: req.Output, Targets: []domain.HintTarget{h.inside(0)}}
		// A matching result afterwards shows: the stale one changed nothing.
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{h.inside(0), h.inside(1)}}
		s := h.labelled(2, 0)
		if len(s.Overlays) != 2 {
			t.Fatalf("labels %+v", s.Overlays)
		}

	})
}

func TestHintErrorCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Err: fmt.Errorf("boom")}
		h.press("x", 0)
		cmd := command(t, h.commands)
		if v, ok := cmd.(ports.ForwardKey); !ok || v.Key.Keysym != "x" {
			t.Fatalf("command %#v", cmd)
		}

	})
}

func TestHintWindowMovedAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		tg := h.inside(0)
		h.show(tg)
		// The window closes before the label is typed: the click must not go
		// anywhere.
		h.client <- ports.WindowUnmapped{ID: 1}
		h.until(func(s domain.Scene) bool { return len(s.Windows) == 0 })
		h.drain()
		h.press("f", 0)
		// With no window left a key goes nowhere; the next bind press, answered
		// by a scan request, shows core handled the label key before it.
		h.press("g", domain.ModSuper)
		receive(t, h.scans)
		for len(h.commands) > 0 {
			if _, ok := (<-h.commands).(ports.PointerButtonTo); ok {
				t.Fatal("button sent to a moved window")
			}
		}

	})
}

// barrier returns the commands core sent for everything given to it so far:
// a marker key goes through the same input channel and owner, and is
// forwarded to the focused window after the commands of the earlier events.
func (h *hintHarness) barrier() []ports.ClientCommand {
	h.t.Helper()
	h.press("marker", 0)
	var out []ports.ClientCommand
	for {
		cmd := command(h.t, h.commands)
		if k, ok := cmd.(ports.ForwardKey); ok && k.Key.Keysym == "marker" {
			return out
		}
		out = append(out, cmd)
	}
}

// buttonsSent drains the commands and counts pointer button commands.
func (h *hintHarness) buttonsSent() (presses, releases int) {
	for _, cmd := range h.barrier() {
		if b, ok := cmd.(ports.PointerButtonTo); ok {
			if b.Pressed {
				presses++
			} else {
				releases++
			}
		}
	}
	return presses, releases
}

// The bind's modifier is still held while the label is typed: the synthetic
// click must reach the client, not start a Cmd+left-button window drag.
func TestHintClickWithBindModifierHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		tg := h.inside(0)
		h.press("g", domain.ModSuper)
		req := receive(t, h.scans)
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
		h.labelled(1, 0)
		h.drain()
		h.press("f", domain.ModSuper) // Super is still down
		h.labelled(0, 0)
		cmds := h.barrier()
		if got, want := commandKinds(cmds), []string{"mods:0", "motion", "press", "release", "mods:restore"}; !slices.Equal(got, want) {
			t.Fatalf("commands %v, want %v", got, want)
		}
		presses, releases := 0, 0
		for _, c := range cmds {
			if b, ok := c.(ports.PointerButtonTo); ok {
				if b.Pressed {
					presses++
				} else {
					releases++
				}
			}
		}
		if presses != 1 || releases != 1 {
			t.Fatalf("client got %d presses, %d releases", presses, releases)
		}
		// No drag is left behind: a later plain motion and a click work as usual.
		h.release("f", domain.ModSuper)
		h.release("Super_L", 0)
		h.send(ports.PointerMotion{X: float64(tg.Rect.X), Y: float64(tg.Rect.Y)})
		h.send(ports.PointerButton{Button: 0x110, Pressed: true})
		h.send(ports.PointerButton{Button: 0x110})
		if presses, releases = h.buttonsSent(); presses != 1 || releases != 1 {
			t.Fatalf("after: %d presses, %d releases (drag left active?)", presses, releases)
		}

	})
}

// constrain gives window 1 a pointer constraint and waits until core applied it.
func (h *hintHarness) constrain(mode domain.ConstraintMode, r domain.Rect) {
	h.client <- ports.PointerConstrained{ID: 1, PointerConstraint: domain.PointerConstraint{Mode: mode, Rect: r}}
	h.client <- ports.WindowMapped{ID: 1} // a publish after the event
	h.until(func(domain.Scene) bool { return true })
}

func TestHintRefusedWithPointerConstraint(t *testing.T) {
	for name, c := range map[string]domain.PointerConstraint{
		"lock":    {Mode: domain.ConstraintLock},
		"confine": {Mode: domain.ConstraintConfine, Rect: domain.Rect{X: 0, Y: 0, W: 5, H: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHint(t, core.Options{})
				h.constrain(c.Mode, c.Rect)
				h.press("g", domain.ModSuper)
				synctest.Wait()
				select {
				case r := <-h.scans:
					t.Fatalf("request under a %s: %+v", name, r)
				default:
				}

			})
		})
	}
}

// A constraint that appears while the labels are shown (the pointer would be
// held or bent away from the target) cancels the click.
func TestHintNoClickWhenConstraintAppears(t *testing.T) {
	for name, c := range map[string]domain.PointerConstraint{
		"lock":    {Mode: domain.ConstraintLock},
		"confine": {Mode: domain.ConstraintConfine, Rect: domain.Rect{X: 0, Y: 0, W: 5, H: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHint(t, core.Options{})
				h.show(h.inside(0))
				h.constrain(c.Mode, c.Rect)
				h.drain()
				h.press("f", 0)
				if p, r := h.buttonsSent(); p+r != 0 {
					t.Fatalf("%d presses, %d releases under a %s", p, r, name)
				}

			})
		})
	}
}

// pressReal sends a real left press over window 1 and waits for the client to
// see it: the window then holds the pointer grab.
func (h *hintHarness) pressReal() {
	h.t.Helper()
	h.send(ports.PointerMotion{X: float64(h.win.X + 1), Y: float64(h.win.Y + 1)})
	h.send(ports.PointerButton{Button: 0x110, Pressed: true})
	for {
		cmd := command(h.t, h.commands)
		if b, ok := cmd.(ports.PointerButtonTo); ok && b.Pressed {
			return
		}
	}
}

func TestHintRefusedWhileButtonHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.pressReal()
		h.press("g", domain.ModSuper)
		synctest.Wait()
		select {
		case r := <-h.scans:
			t.Fatalf("request with a button held: %+v", r)
		default:
		}

	})
}

// A button pressed (and so a grab taken) after the labels appeared: the label
// is typed, no synthetic button goes out and the real press stays held.
func TestHintNoClickWhileGrabbed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		h.pressReal()
		h.press("f", 0)
		if p, r := h.buttonsSent(); p+r != 0 {
			t.Fatalf("synthetic click during a grab: %d presses, %d releases", p, r)
		}
		// The real release still reaches the client: c.buttons was not touched.
		h.send(ports.PointerButton{Button: 0x110})
		if _, r := h.buttonsSent(); r != 1 {
			t.Fatalf("real release delivered %d times", r)
		}

	})
}

// The window stays mapped but moves away from the target's centre.
func TestHintMappedWindowMovedAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		// A target at the right edge of window 1.
		tg := domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + h.win.W - 50, Y: h.win.Y + 10, W: 40, H: 10}}
		h.show(tg)
		h.client <- ports.WindowMapped{ID: 2}
		s := h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 && len(s.Overlays) <= 1 })
		if s.Windows[0].Rect == h.win {
			t.Fatalf("window 1 did not move: %+v", s.Windows)
		}
		h.drain()
		h.press("f", 0)
		if p, r := h.buttonsSent(); p+r != 0 {
			t.Fatalf("click went to %d presses %d releases after the window moved", p, r)
		}

	})
}

// commandsSent drains the commands up to a barrier, keeping the ones that
// matter for a click: focus, motion and buttons.
func (h *hintHarness) commandsSent() []ports.ClientCommand {
	var out []ports.ClientCommand
	for _, cmd := range h.barrier() {
		switch cmd.(type) {
		case ports.FocusWindow, ports.SetModifiers, ports.PointerMotionTo, ports.PointerButtonTo:
			out = append(out, cmd)
		}
	}
	return out
}

func commandKinds(cmds []ports.ClientCommand) []string {
	var out []string
	for _, c := range cmds {
		switch v := c.(type) {
		case ports.FocusWindow:
			out = append(out, fmt.Sprintf("focus:%d", v.ID))
		case ports.SetModifiers:
			if v.Restore {
				out = append(out, "mods:restore")
			} else {
				out = append(out, fmt.Sprintf("mods:%d", v.Mods))
			}
		case ports.PointerMotionTo:
			out = append(out, "motion")
		case ports.PointerButtonTo:
			if v.Pressed {
				out = append(out, "press")
			} else {
				out = append(out, "release")
			}
		}
	}
	return out
}

// A click on the focused window moves the pointer to the target and presses
// and releases the left button; it states no modifiers.
func TestHintClickSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		h.drain()
		h.press("f", 0)
		got := commandKinds(h.commandsSent())
		want := []string{"mods:0", "motion", "press", "release", "mods:restore"}
		if !slices.Equal(got, want) {
			t.Fatalf("commands %v, want %v", got, want)
		}

	})
}

// A window that moved while the scan ran: a target over it is dropped.
func TestHintResultDroppedWhenWindowMoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		tg := h.inside(0)
		h.client <- ports.WindowMapped{ID: 2}
		h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 })
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
		// Whatever window now covers the target was not scanned: no labels.
		if s := h.labelled(0, 0); len(s.Overlays) != 0 {
			t.Fatalf("labels %+v", s.Overlays)
		}

	})
}

// A new window covering the target (not in the snapshot) drops it as well.
func TestHintResultDroppedForUnseenWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		h.client <- ports.WindowMapped{ID: 2}
		s := h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 })
		var tg domain.HintTarget
		for _, w := range s.Windows {
			if w.ID == 2 {
				tg.Rect = domain.Rect{X: w.Rect.X + 5, Y: w.Rect.Y + 5, W: 20, H: 10}
			}
		}
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
		if s := h.labelled(0, 0); len(s.Overlays) != 0 {
			t.Fatalf("labels %+v", s.Overlays)
		}

	})
}

// Targets outside the output are dropped.
func TestHintDropsTargetsOutsideOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		req := h.start()
		out := domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: 2000, Y: 10, W: 10, H: 10}}
		h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{out, h.inside(0)}}
		if s := h.labelled(1, 0); len(s.Overlays) != 1 {
			t.Fatalf("labels %+v", s.Overlays)
		}

	})
}

// The output changing size while labels are shown ends hint mode.
func TestHintCancelsWhenOutputChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		h.output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 1200, Height: 800}}
		if s := h.labelled(0, 0); len(s.Overlays) != 0 {
			t.Fatalf("labels %+v", s.Overlays)
		}

	})
}

// A window that does not have the keyboard gets it before the click.
func TestHintClickFocusesWindowFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.client <- ports.WindowMapped{ID: 2}
		s := h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 })
		var r domain.Rect
		for _, w := range s.Windows {
			if w.ID == 1 {
				r = w.Rect
			}
		}
		h.win = r
		h.show(h.inside(0))
		h.drain()
		h.press("f", 0)
		got := commandKinds(h.commandsSent())
		want := []string{"focus:1", "mods:0", "motion", "press", "release", "mods:restore"}
		if !slices.Equal(got, want) {
			t.Fatalf("commands %v, want %v", got, want)
		}

	})
}

// A click that is no longer allowed sends no command at all.
func TestHintClickNothingWhenNotClickable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		h.constrain(domain.ConstraintLock, domain.Rect{})
		h.drain()
		h.press("f", 0)
		if got := commandKinds(h.commandsSent()); len(got) != 0 {
			t.Fatalf("commands %v under a constraint", got)
		}

	})
}

func TestHintDisabledNotesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := scrollDefaults()
		cfg.Binds["Cmd+g"] = "hint"
		var noted atomic.Int32
		h := &hintHarness{
			input:    make(chan ports.InputEvent, 32),
			client:   make(chan ports.ClientEvent, 32),
			output:   make(chan ports.OutputEvent, 8),
			commands: make(chan ports.ClientCommand, 256),
			scenes:   make(chan []domain.Scene, 1),
		}
		ch := core.Channels{Client: h.client, Input: h.input, Output: h.output, Commands: h.commands, Scenes: h.scenes}
		c, err := core.New(cfg, ch, testOptions(t, core.Options{HintDisabled: func() { noted.Add(1) }}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		t.Cleanup(func() { cancel(); receive(t, done) })
		h.output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
		scene(t, h.scenes)
		for range 3 {
			h.press("g", domain.ModSuper)
			h.release("g", 0)
		}
		c.Ping(ctx)
		if got := noted.Load(); got != 1 {
			t.Fatalf("disabled note ran %d times, want 1", got)
		}

	})
}

// Resizing window 1 leaves the old target centre inside it. Identity alone
// therefore cannot protect either admission or the eventual synthetic click.
func TestHintRejectsResizedSource(t *testing.T) {
	{
		for _, beforeResult := range []bool{false, true} {
			t.Run(fmt.Sprintf("beforeResult=%v", beforeResult), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := newHint(t, core.Options{})
					tg := h.inside(0)
					req := h.start()
					if !beforeResult {
						h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
						h.labelled(1, 0)
					}
					h.client <- ports.WindowMapped{ID: 2}
					s := h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 })
					var resized domain.Rect
					for _, w := range s.Windows {
						if w.ID == 1 {
							resized = w.Rect
						}
					}
					x, y := tg.Rect.X+tg.Rect.W/2, tg.Rect.Y+tg.Rect.H/2
					if resized == h.win || x < resized.X || x >= resized.X+resized.W || y < resized.Y || y >= resized.Y+resized.H {
						t.Fatalf("setup: old target %+v not inside resized window %+v (was %+v)", tg.Rect, resized, h.win)
					}
					if beforeResult {
						h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
						h.labelled(0, 0)
					} else {
						h.drain()
						h.press("f", 0)
						for _, cmd := range h.barrier() {
							if _, ok := cmd.(ports.PointerButtonTo); ok {
								t.Fatal("clicked resized source window")
							}
						}
					}

				})
			})
		}
	}
}

// hint.capture reaches the scene next to the labels, off by default.
func TestHintCaptureReachesScene(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, on := range []bool{false, true} {
			h := newHintConfig(t, core.Options{}, func(cfg *domain.Config) { cfg.Hint.Capture = on })
			s := h.show(h.inside(0))
			if len(s.Overlays) == 0 {
				t.Fatal("no overlays")
			}
			for _, o := range s.Overlays {
				if o.Capture != on {
					t.Errorf("hint.capture %v: overlay has %v", on, o.Capture)
				}
			}
		}

	})
}

// Labels hang off the targets' top-left corners: targets whose corners are
// closer than a label drop, the lowest score first, whatever the input order.
func TestHintOverlappingLabelsDropLowerScore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		at := func(h *hintHarness, dx, dy int, score float32) domain.HintTarget {
			return domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + 20 + dx, Y: h.win.Y + 20 + dy, W: 80, H: 30}, Score: score}
		}
		for _, rev := range []bool{false, true} {
			h := newHint(t, core.Options{})
			ts := []domain.HintTarget{
				at(h, 0, 0, 0.4),    // clashes with the next one, loses
				at(h, 3, 2, 0.9),    // wins
				at(h, 300, 0, 0.1),  // far away: kept, whatever its score
				at(h, 0, 200, 0.5),  // far below
				at(h, 4, 200, 0.45), // clashes with the previous one
			}
			if rev {
				slices.Reverse(ts)
			}
			req := h.start()
			h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: ts}
			s := h.labelled(3, 0)
			var got [][2]int
			for _, l := range s.Overlays {
				got = append(got, [2]int{l.Rect.X - h.win.X - 20, l.Rect.Y - h.win.Y - 20})
			}
			want := [][2]int{{300, 0}, {3, 2}, {0, 200}} // sorted by Y, then X
			if !slices.Equal(got, want) {
				t.Fatalf("rev=%v: label corners %v, want %v", rev, got, want)
			}
		}

	})
}

func TestHintSizeReloadCancelsShownHints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		h.show(h.inside(0))
		cfg := h.cfg
		cfg.Hint.Size = 40
		h.config <- domain.ConfigChanged{Config: cfg}
		h.until(func(s domain.Scene) bool { return len(s.Overlays) == 0 })
		s := h.show(h.inside(0))
		if len(s.Overlays) == 0 {
			t.Fatal("no overlays")
		}
		for _, o := range s.Overlays {
			if o.Style.Size != 40 {
				t.Fatalf("overlay size %v, want 40", o.Style.Size)
			}
		}

	})
}

func TestHintSourceClickModifiers(t *testing.T) {
	for _, source := range []domain.HintSource{domain.HintSourceATSPI, domain.HintSourceOCR, domain.HintSourceDetector} {
		t.Run(source.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHintConfig(t, core.Options{}, func(c *domain.Config) { c.Hint.TextClick = domain.ModCtrl | domain.ModShift })
				tg := h.inside(0)
				tg.Source = source
				h.show(tg)
				h.drain()
				h.press("f", 0)
				var got []domain.Mods
				for _, cmd := range h.commandsSent() {
					if m, ok := cmd.(ports.SetModifiers); ok && !m.Restore {
						got = append(got, m.Mods)
					}
				}
				want := domain.Mods(0)
				if source == domain.HintSourceOCR {
					want = domain.ModCtrl | domain.ModShift
				}
				if len(got) != 1 || got[0] != want {
					t.Fatal(got, want)
				}

			})
		})
	}
}
func TestHintDropsWrongWindow(t *testing.T) {
	for _, id := range []domain.WindowID{0, 2} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHint(t, core.Options{})
				req := h.start()
				tg := h.inside(0)
				tg.Window = id
				h.hints <- domain.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []domain.HintTarget{tg}}
				h.labelled(0, 0)

			})
		})
	}
}
func TestHintRequestPopupUnits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHintConfig(t, core.Options{}, func(c *domain.Config) { c.Hint.LinksOnly = true })
		h.client <- ports.WindowMapped{ID: 2}
		h.until(func(s domain.Scene) bool { return len(s.Windows) == 2 })
		popupRects := map[domain.WindowID]domain.Rect{}
		for _, p := range []struct{ id, parent domain.WindowID }{{10, 1}, {11, 10}, {20, 2}} {
			h.client <- ports.PopupRequest{ID: p.id, Parent: p.parent, Positioner: domain.Positioner{Width: 30, Height: 20, AnchorRect: domain.Rect{W: 1, H: 1}}}
			h.client <- ports.PopupMapped{ID: p.id}
			h.until(func(s domain.Scene) bool {
				for _, w := range s.Windows {
					if w.ID == p.id {
						popupRects[p.id] = w.Rect
						return true
					}
				}
				return false
			})
		}
		req := h.start()
		if !req.LinksOnly || len(req.Windows) != 2 {
			t.Fatal(req)
		}
		for _, w := range req.Windows {
			expected := []domain.Rect{popupRects[20]}
			if w.ID == 1 {
				expected = []domain.Rect{popupRects[10], popupRects[11]}
			}
			if !slices.Equal(w.Popups, expected) {
				t.Fatalf("unit %d popups %v, want %v", w.ID, w.Popups, expected)
			}
			want := 1
			if w.ID == 1 {
				want = 2
			}
			if len(w.Popups) != want {
				t.Fatal(w)
			}
			for _, r := range w.Popups {
				if r.W != 30 || r.H != 20 {
					t.Fatal(r)
				}
			}
		}

	})
}

func hintTargets27(h *hintHarness) []domain.HintTarget {
	var targets []domain.HintTarget
	for i := 0; i < 27; i++ {
		targets = append(targets, domain.HintTarget{Window: 1, Source: domain.HintSourceDetector, Rect: domain.Rect{X: h.win.X + 5 + 100*(i%4), Y: h.win.Y + 5 + 40*(i/4), W: 30, H: 10}})
	}
	return targets
}

func TestHintOverlays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		s := h.show(hintTargets27(h)...)
		if len(s.Overlays) != 27 {
			t.Fatalf("%d overlays", len(s.Overlays))
		}
		ids := map[uint64]bool{}
		for _, o := range s.Overlays {
			if o.Kind != domain.OverlayLabel || o.Anchor != domain.AnchorOutsideTopLeft {
				t.Fatalf("overlay %+v", o)
			}
			if o.ID == 0 || ids[o.ID] {
				t.Fatalf("overlay id %d zero or repeated", o.ID)
			}
			ids[o.ID] = true
		}
	})
}

func TestHintOverlaysTypingKeepsIDChangesSeq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		before := h.show(hintTargets27(h)...)
		seqs := map[uint64]uint64{}
		for _, o := range before.Overlays {
			seqs[o.ID] = o.Seq
		}
		h.press("f", 0)
		after := h.labelled(26, 1)
		if len(after.Overlays) != 26 {
			t.Fatalf("%d overlays after f", len(after.Overlays))
		}
		for _, o := range after.Overlays {
			old, ok := seqs[o.ID]
			if !ok || old == o.Seq {
				t.Fatalf("overlay %+v: id known %v, seq %d before", o, ok, old)
			}
		}
	})
}

func TestHintOverlaysNewScanChangesSeq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		first := h.show(hintTargets27(h)...)
		h.press("Escape", 0)
		h.labelled(0, 0)
		second := h.show(hintTargets27(h)...)
		if len(first.Overlays) == 0 || len(first.Overlays) != len(second.Overlays) {
			t.Fatalf("%d then %d overlays", len(first.Overlays), len(second.Overlays))
		}
		for i, o := range second.Overlays {
			if o.ID != first.Overlays[i].ID || o.Seq == first.Overlays[i].Seq {
				t.Fatalf("overlay %+v, first scan %+v", o, first.Overlays[i])
			}
		}
	})
}

func TestScenePhysicalFromOutputInfo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHint(t, core.Options{})
		s := h.show(h.inside(0))
		p := s.Physical
		if p.Width == 0 || p.Height == 0 {
			t.Fatalf("physical %+v", p)
		}
	})
}
