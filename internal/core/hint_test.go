package core_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

type hintHarness struct {
	t        *testing.T
	input    chan ports.InputEvent
	client   chan ports.ClientEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	scenes   chan []ports.Scene
	scans    chan ports.HintScanRequest
	hints    chan ports.HintScanResult
	win      ports.Rect // window 1, output-local
	// state is the security state input is stamped with.
	state  ports.SecurityState
	config chan ports.ConfigChanged
	cfg    ports.Config
}

// newHint starts a core with one 1000x800 output "OUT-1" and window 1 mapped.
func newHint(t *testing.T, opts core.Options) *hintHarness {
	t.Helper()
	return newHintConfig(t, opts, func(*ports.Config) {})
}

// newHintConfig is newHint with the config edited by edit.
func newHintConfig(t *testing.T, opts core.Options, edit func(*ports.Config)) *hintHarness {
	t.Helper()
	cfg := scrollDefaults()
	edit(&cfg)
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 20
	cfg.Binds["Cmd+g"] = "hint"
	h := &hintHarness{
		config:   make(chan ports.ConfigChanged),
		cfg:      cfg,
		t:        t,
		input:    make(chan ports.InputEvent, 32),
		client:   make(chan ports.ClientEvent, 32),
		output:   make(chan ports.OutputEvent, 8),
		commands: make(chan ports.ClientCommand, 256),
		scenes:   make(chan []ports.Scene, 1),
		scans:    make(chan ports.HintScanRequest, 1),
		// Unbuffered: a send returns once core took the result.
		hints: make(chan ports.HintScanResult),
	}
	ch := core.Channels{Config: h.config, Client: h.client, Input: h.input, Output: h.output, Commands: h.commands, Scenes: h.scenes, HintScans: h.scans, Hints: h.hints}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); receive(t, done) })
	h.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
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
func (h *hintHarness) press(sym string, mods ports.Mods) {
	h.send(ports.KeyEvent{Keysym: sym, Base: sym, Mods: mods, Pressed: true})
}

func (h *hintHarness) release(sym string, mods ports.Mods) {
	h.send(ports.KeyEvent{Keysym: sym, Base: sym, Mods: mods})
}

// send gives core an input event stamped with the current security state.
func (h *hintHarness) send(ev ports.InputEvent) {
	h.input <- ports.SecurityInput{State: h.state, Event: ev}
}

// start presses the hint bind and returns the request.
func (h *hintHarness) start() ports.HintScanRequest {
	h.t.Helper()
	h.press("g", ports.ModSuper)
	req := receive(h.t, h.scans)
	h.release("g", 0) // the user lets go of the whole chord
	return req
}

// show starts hint mode and answers with targets, returning the next scene.
func (h *hintHarness) show(targets ...ports.HintTarget) ports.Scene {
	h.t.Helper()
	req := h.start()
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: targets}
	return h.labelled(len(targets), 0)
}

// until returns the first scene, from the next one on, that satisfies ok:
// the bind press and other input publish scenes before the one waited for.
func (h *hintHarness) until(ok func(ports.Scene) bool) ports.Scene {
	h.t.Helper()
	for {
		if s := scene(h.t, h.scenes); ok(s) {
			return s
		}
	}
}

// labelled waits for a scene with n labels, all typed with typed letters.
func (h *hintHarness) labelled(n, typed int) ports.Scene {
	h.t.Helper()
	return h.until(func(s ports.Scene) bool {
		return len(s.HintLabels) == n && (n == 0 || s.HintLabels[0].Typed == typed)
	})
}

// inside is a target of the given size inside window 1.
func (h *hintHarness) inside(i int) ports.HintTarget {
	return ports.HintTarget{Rect: ports.Rect{X: h.win.X + 10 + 100*(i%4), Y: h.win.Y + 10 + 40*(i/4), W: 40, H: 10}}
}

func TestHintRequestOnBind(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	if req.Output != "OUT-1" || req.Width != 1000 || req.Height != 800 || req.Scale != 1 || req.Seq == 0 {
		t.Fatalf("request %+v", req)
	}
	// A second bind while pending does not ask again; the pending phase
	// cancels on any key.
	h.press("g", ports.ModSuper)
	select {
	case r := <-h.scans:
		t.Fatalf("second request %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHintRequestNilScansIsNoop(t *testing.T) {
	cfg := scrollDefaults()
	cfg.Binds["Cmd+g"] = "hint"
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 4)
	output := make(chan ports.OutputEvent, 4)
	commands := make(chan ports.ClientCommand, 32)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() { cancel(); receive(t, done) }()
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
	scene(t, scenes)
	input <- ports.KeyEvent{Keysym: "g", Base: "g", Mods: ports.ModSuper, Pressed: true}
	if s := scene(t, scenes); len(s.HintLabels) != 0 {
		t.Fatal(s.HintLabels)
	}
}

func TestHintRefusedWhenProtected(t *testing.T) {
	var state atomic.Pointer[ports.SecurityState]
	state.Store(&ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return *state.Load() }).Maybe()
	h := newHint(t, core.Options{Security: gate})
	// Hint mode shown, then the session locks: the labels go away.
	h.show(h.inside(0))
	state.Store(&ports.SecurityState{Generation: 2, Protected: true})
	h.state = *state.Load()
	h.client <- ports.WindowMapped{ID: 2}
	s := h.until(func(s ports.Scene) bool { return s.Security.Protected })
	if len(s.HintLabels) != 0 {
		t.Fatalf("labels while protected %+v", s.HintLabels)
	}
	// And a new request is refused while it lasts.
	h.press("g", ports.ModSuper)
	select {
	case r := <-h.scans:
		t.Fatalf("request while protected %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHintResultShowsLabels(t *testing.T) {
	h := newHint(t, core.Options{})
	var targets []ports.HintTarget
	for i := 0; i < 26; i++ {
		targets = append(targets, h.inside(i))
	}
	s := h.show(targets...)
	if len(s.HintLabels) != 26 {
		t.Fatalf("%d labels", len(s.HintLabels))
	}
	seen := map[string]bool{}
	for _, l := range s.HintLabels {
		if len(l.Text) != 1 || l.Typed != 0 || seen[l.Text] {
			t.Fatalf("label %+v", l)
		}
		seen[l.Text] = true
	}
	// Sorted by position: the first target gets the first letter.
	if s.HintLabels[0].Text != "f" || s.HintLabels[0].Rect.X != targets[0].Rect.X || s.HintLabels[0].Rect.Y != targets[0].Rect.Y {
		t.Fatalf("first %+v", s.HintLabels[0])
	}
}

func TestHintTwoLetterLabels(t *testing.T) {
	h := newHint(t, core.Options{})
	var targets []ports.HintTarget
	for i := 0; i < 27; i++ {
		targets = append(targets, ports.HintTarget{Rect: ports.Rect{X: h.win.X + 5 + 100*(i%4), Y: h.win.Y + 5 + 40*(i/4), W: 30, H: 10}})
	}
	s := h.show(targets...)
	if len(s.HintLabels) != 27 {
		t.Fatalf("%d labels", len(s.HintLabels))
	}
	seen := map[string]bool{}
	for _, l := range s.HintLabels {
		if len(l.Text) != 2 || seen[l.Text] {
			t.Fatalf("label %+v", l)
		}
		seen[l.Text] = true
	}
	if s.HintLabels[0].Text != "ff" || s.HintLabels[1].Text != "fj" {
		t.Fatalf("labels %v %v", s.HintLabels[0].Text, s.HintLabels[1].Text)
	}
}

func TestHintDropsTargetsOutsideWindows(t *testing.T) {
	h := newHint(t, core.Options{})
	in := h.inside(0)
	// In the gap around the window, over the background.
	out := ports.HintTarget{Rect: ports.Rect{X: 2, Y: 2, W: 4, H: 4}}
	if h.win.X < 8 || h.win.Y < 8 {
		t.Fatalf("window %+v leaves no gap", h.win)
	}
	req := h.start()
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{out, in}}
	s := h.labelled(1, 0)
	if len(s.HintLabels) != 1 || s.HintLabels[0].Rect.X != in.Rect.X {
		t.Fatalf("labels %+v (window %+v)", s.HintLabels, h.win)
	}
}

func TestHintTypingFilters(t *testing.T) {
	h := newHint(t, core.Options{})
	var targets []ports.HintTarget
	for i := 0; i < 27; i++ {
		targets = append(targets, ports.HintTarget{Rect: ports.Rect{X: h.win.X + 5 + 100*(i%4), Y: h.win.Y + 5 + 40*(i/4), W: 30, H: 10}})
	}
	h.show(targets...)
	h.press("f", 0)
	s := h.labelled(26, 1)
	if len(s.HintLabels) != 26 {
		t.Fatalf("%d labels after f", len(s.HintLabels))
	}
	for _, l := range s.HintLabels {
		if l.Typed != 1 || l.Text[0] != 'f' {
			t.Fatalf("label %+v", l)
		}
	}
	h.press("BackSpace", 0)
	h.labelled(27, 0)
	// Only the 27th target starts with j: its label is "jf".
	h.press("j", 0)
	s = h.labelled(1, 1)
	if len(s.HintLabels) != 1 || s.HintLabels[0].Text != "jf" {
		t.Fatalf("after j %+v", s.HintLabels)
	}
}

func TestHintClick(t *testing.T) {
	h := newHint(t, core.Options{})
	tg := h.inside(0)
	h.show(tg)
	h.drain()
	h.press("f", 0)
	s := h.labelled(0, 0)
	if len(s.HintLabels) != 0 {
		t.Fatalf("labels after click %+v", s.HintLabels)
	}
	cx := float64(tg.Rect.X) + float64(tg.Rect.W)/2 - float64(h.win.X)
	cy := float64(tg.Rect.Y) + float64(tg.Rect.H)/2 - float64(h.win.Y)
	var gotMotion, gotPress, gotRelease, gotFocus bool
	for len(h.commands) > 0 || !gotRelease {
		var cmd ports.ClientCommand
		select {
		case cmd = <-h.commands:
		case <-time.After(time.Second):
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
}

func TestHintEscapeCancels(t *testing.T) {
	h := newHint(t, core.Options{})
	h.show(h.inside(0))
	h.drain()
	h.press("Escape", 0)
	s := h.labelled(0, 0)
	if len(s.HintLabels) != 0 {
		t.Fatalf("labels %+v", s.HintLabels)
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
}

func TestHintKeysNotForwarded(t *testing.T) {
	h := newHint(t, core.Options{})
	h.show(h.inside(0), h.inside(1))
	h.drain()
	// A letter that matches nothing is swallowed and does not cancel.
	h.press("q", 0)
	h.release("q", 0)
	// Any other key cancels and is swallowed.
	h.press("Return", 0)
	s := h.labelled(0, 0)
	if len(s.HintLabels) != 0 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
	h.release("Return", 0)
	h.press("x", 0)
	cmd := command(t, h.commands)
	if v, ok := cmd.(ports.ForwardKey); !ok || v.Key.Keysym != "x" {
		t.Fatalf("first forwarded command %#v", cmd)
	}
}

func TestHintStaleSeqIgnored(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	h.hints <- ports.HintScanResult{Seq: req.Seq + 7, Output: req.Output, Targets: []ports.HintTarget{h.inside(0)}}
	// A matching result afterwards shows: the stale one changed nothing.
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{h.inside(0), h.inside(1)}}
	s := h.labelled(2, 0)
	if len(s.HintLabels) != 2 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
}

func TestHintErrorCancels(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Err: fmt.Errorf("boom")}
	h.press("x", 0)
	cmd := command(t, h.commands)
	if v, ok := cmd.(ports.ForwardKey); !ok || v.Key.Keysym != "x" {
		t.Fatalf("command %#v", cmd)
	}
}

func TestHintWindowMovedAborts(t *testing.T) {
	h := newHint(t, core.Options{})
	tg := h.inside(0)
	h.show(tg)
	// The window closes before the label is typed: the click must not go
	// anywhere.
	h.client <- ports.WindowUnmapped{ID: 1}
	h.until(func(s ports.Scene) bool { return len(s.Windows) == 0 })
	h.drain()
	h.press("f", 0)
	// With no window left a key goes nowhere; the next bind press, answered
	// by a scan request, shows core handled the label key before it.
	h.press("g", ports.ModSuper)
	receive(t, h.scans)
	for len(h.commands) > 0 {
		if _, ok := (<-h.commands).(ports.PointerButtonTo); ok {
			t.Fatal("button sent to a moved window")
		}
	}
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
	h := newHint(t, core.Options{})
	tg := h.inside(0)
	h.press("g", ports.ModSuper)
	req := receive(t, h.scans)
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{tg}}
	h.labelled(1, 0)
	h.drain()
	h.press("f", ports.ModSuper) // Super is still down
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
	h.release("f", ports.ModSuper)
	h.release("Super_L", 0)
	h.send(ports.PointerMotion{X: float64(tg.Rect.X), Y: float64(tg.Rect.Y)})
	h.send(ports.PointerButton{Button: 0x110, Pressed: true})
	h.send(ports.PointerButton{Button: 0x110})
	if presses, releases = h.buttonsSent(); presses != 1 || releases != 1 {
		t.Fatalf("after: %d presses, %d releases (drag left active?)", presses, releases)
	}
}

// constrain gives window 1 a pointer constraint and waits until core applied it.
func (h *hintHarness) constrain(mode ports.ConstraintMode, r ports.Rect) {
	h.client <- ports.PointerConstrained{ID: 1, PointerConstraint: ports.PointerConstraint{Mode: mode, Rect: r}}
	h.client <- ports.WindowMapped{ID: 1} // a publish after the event
	h.until(func(ports.Scene) bool { return true })
}

func TestHintRefusedWithPointerConstraint(t *testing.T) {
	for name, c := range map[string]ports.PointerConstraint{
		"lock":    {Mode: ports.ConstraintLock},
		"confine": {Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 0, Y: 0, W: 5, H: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHint(t, core.Options{})
			h.constrain(c.Mode, c.Rect)
			h.press("g", ports.ModSuper)
			select {
			case r := <-h.scans:
				t.Fatalf("request under a %s: %+v", name, r)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// A constraint that appears while the labels are shown (the pointer would be
// held or bent away from the target) cancels the click.
func TestHintNoClickWhenConstraintAppears(t *testing.T) {
	for name, c := range map[string]ports.PointerConstraint{
		"lock":    {Mode: ports.ConstraintLock},
		"confine": {Mode: ports.ConstraintConfine, Rect: ports.Rect{X: 0, Y: 0, W: 5, H: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHint(t, core.Options{})
			h.show(h.inside(0))
			h.constrain(c.Mode, c.Rect)
			h.drain()
			h.press("f", 0)
			if p, r := h.buttonsSent(); p+r != 0 {
				t.Fatalf("%d presses, %d releases under a %s", p, r, name)
			}
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
	h := newHint(t, core.Options{})
	h.pressReal()
	h.press("g", ports.ModSuper)
	select {
	case r := <-h.scans:
		t.Fatalf("request with a button held: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}

// A button pressed (and so a grab taken) after the labels appeared: the label
// is typed, no synthetic button goes out and the real press stays held.
func TestHintNoClickWhileGrabbed(t *testing.T) {
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
}

// The window stays mapped but moves away from the target's centre.
func TestHintMappedWindowMovedAborts(t *testing.T) {
	h := newHint(t, core.Options{})
	// A target at the right edge of window 1.
	tg := ports.HintTarget{Rect: ports.Rect{X: h.win.X + h.win.W - 50, Y: h.win.Y + 10, W: 40, H: 10}}
	h.show(tg)
	h.client <- ports.WindowMapped{ID: 2}
	s := h.until(func(s ports.Scene) bool { return len(s.Windows) == 2 && len(s.HintLabels) <= 1 })
	if s.Windows[0].Rect == h.win {
		t.Fatalf("window 1 did not move: %+v", s.Windows)
	}
	h.drain()
	h.press("f", 0)
	if p, r := h.buttonsSent(); p+r != 0 {
		t.Fatalf("click went to %d presses %d releases after the window moved", p, r)
	}
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
	h := newHint(t, core.Options{})
	h.show(h.inside(0))
	h.drain()
	h.press("f", 0)
	got := commandKinds(h.commandsSent())
	want := []string{"mods:0", "motion", "press", "release", "mods:restore"}
	if !slices.Equal(got, want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
}

// A window that moved while the scan ran: a target over it is dropped.
func TestHintResultDroppedWhenWindowMoved(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	tg := h.inside(0)
	h.client <- ports.WindowMapped{ID: 2}
	h.until(func(s ports.Scene) bool { return len(s.Windows) == 2 })
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{tg}}
	// Whatever window now covers the target was not scanned: no labels.
	if s := h.labelled(0, 0); len(s.HintLabels) != 0 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
}

// A new window covering the target (not in the snapshot) drops it as well.
func TestHintResultDroppedForUnseenWindow(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	h.client <- ports.WindowMapped{ID: 2}
	s := h.until(func(s ports.Scene) bool { return len(s.Windows) == 2 })
	var tg ports.HintTarget
	for _, w := range s.Windows {
		if w.ID == 2 {
			tg.Rect = ports.Rect{X: w.Rect.X + 5, Y: w.Rect.Y + 5, W: 20, H: 10}
		}
	}
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{tg}}
	if s := h.labelled(0, 0); len(s.HintLabels) != 0 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
}

// Targets outside the output are dropped.
func TestHintDropsTargetsOutsideOutput(t *testing.T) {
	h := newHint(t, core.Options{})
	req := h.start()
	out := ports.HintTarget{Rect: ports.Rect{X: 2000, Y: 10, W: 10, H: 10}}
	h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{out, h.inside(0)}}
	if s := h.labelled(1, 0); len(s.HintLabels) != 1 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
}

// The output changing size while labels are shown ends hint mode.
func TestHintCancelsWhenOutputChanges(t *testing.T) {
	h := newHint(t, core.Options{})
	h.show(h.inside(0))
	h.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 1200, Height: 800}}
	if s := h.labelled(0, 0); len(s.HintLabels) != 0 {
		t.Fatalf("labels %+v", s.HintLabels)
	}
}

// A window that does not have the keyboard gets it before the click.
func TestHintClickFocusesWindowFirst(t *testing.T) {
	h := newHint(t, core.Options{})
	h.client <- ports.WindowMapped{ID: 2}
	s := h.until(func(s ports.Scene) bool { return len(s.Windows) == 2 })
	var r ports.Rect
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
}

// A click that is no longer allowed sends no command at all.
func TestHintClickNothingWhenNotClickable(t *testing.T) {
	h := newHint(t, core.Options{})
	h.show(h.inside(0))
	h.constrain(ports.ConstraintLock, ports.Rect{})
	h.drain()
	h.press("f", 0)
	if got := commandKinds(h.commandsSent()); len(got) != 0 {
		t.Fatalf("commands %v under a constraint", got)
	}
}

func TestHintDisabledNotesOnce(t *testing.T) {
	cfg := scrollDefaults()
	cfg.Binds["Cmd+g"] = "hint"
	var noted atomic.Int32
	h := &hintHarness{
		input:    make(chan ports.InputEvent, 32),
		client:   make(chan ports.ClientEvent, 32),
		output:   make(chan ports.OutputEvent, 8),
		commands: make(chan ports.ClientCommand, 256),
		scenes:   make(chan []ports.Scene, 1),
	}
	ch := core.Channels{Client: h.client, Input: h.input, Output: h.output, Commands: h.commands, Scenes: h.scenes}
	c, err := core.New(cfg, ch, core.Options{HintDisabled: func() { noted.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); receive(t, done) })
	h.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800}}
	scene(t, h.scenes)
	for range 3 {
		h.press("g", ports.ModSuper)
		h.release("g", 0)
	}
	c.Ping(ctx)
	if got := noted.Load(); got != 1 {
		t.Fatalf("disabled note ran %d times, want 1", got)
	}
}

// Resizing window 1 leaves the old target centre inside it. Identity alone
// therefore cannot protect either admission or the eventual synthetic click.
func TestHintRejectsResizedSource(t *testing.T) {
	{
		for _, beforeResult := range []bool{false, true} {
			t.Run(fmt.Sprintf("beforeResult=%v", beforeResult), func(t *testing.T) {
				h := newHint(t, core.Options{})
				tg := h.inside(0)
				req := h.start()
				if !beforeResult {
					h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{tg}}
					h.labelled(1, 0)
				}
				h.client <- ports.WindowMapped{ID: 2}
				s := h.until(func(s ports.Scene) bool { return len(s.Windows) == 2 })
				var resized ports.Rect
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
					h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: []ports.HintTarget{tg}}
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
		}
	}
}

// hint.capture reaches the scene next to the labels, off by default.
func TestHintCaptureReachesScene(t *testing.T) {
	for _, on := range []bool{false, true} {
		h := newHintConfig(t, core.Options{}, func(cfg *ports.Config) { cfg.Hint.Capture = on })
		s := h.show(h.inside(0))
		if s.HintStyle.Capture != on {
			t.Errorf("hint.capture %v: scene has %v", on, s.HintStyle.Capture)
		}
	}
}

// Labels hang off the targets' top-left corners: targets whose corners are
// closer than a label drop, the lowest score first, whatever the input order.
func TestHintOverlappingLabelsDropLowerScore(t *testing.T) {
	at := func(h *hintHarness, dx, dy int, score float32) ports.HintTarget {
		return ports.HintTarget{Rect: ports.Rect{X: h.win.X + 20 + dx, Y: h.win.Y + 20 + dy, W: 80, H: 30}, Score: score}
	}
	for _, rev := range []bool{false, true} {
		h := newHint(t, core.Options{})
		ts := []ports.HintTarget{
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
		h.hints <- ports.HintScanResult{Seq: req.Seq, Output: req.Output, Targets: ts}
		s := h.labelled(3, 0)
		var got [][2]int
		for _, l := range s.HintLabels {
			got = append(got, [2]int{l.Rect.X - h.win.X - 20, l.Rect.Y - h.win.Y - 20})
		}
		want := [][2]int{{300, 0}, {3, 2}, {0, 200}} // sorted by Y, then X
		if !slices.Equal(got, want) {
			t.Fatalf("rev=%v: label corners %v, want %v", rev, got, want)
		}
	}
}

func TestHintSizeReloadCancelsShownHints(t *testing.T) {
	h := newHint(t, core.Options{})
	h.show(h.inside(0))
	cfg := h.cfg
	cfg.Hint.Size = 40
	h.config <- ports.ConfigChanged{Config: cfg}
	h.until(func(s ports.Scene) bool { return s.HintStyle.Size == 40 && len(s.HintLabels) == 0 })
}
