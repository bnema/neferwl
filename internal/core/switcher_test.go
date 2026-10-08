package core

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// threeColumns is a workspace of three one-window columns, windows 1..3.
func threeColumns() *Workspace {
	w := workspace()
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	return w
}

func focusNote(w *Workspace, id WindowID) {
	w.FocusID(id)
	w.noteFocus()
}

func TestMRUOrder(t *testing.T) {
	tests := []struct {
		name string
		run  func(w *Workspace)
		want []WindowID
	}{
		{"follows focus", func(w *Workspace) {
			// Columns 0, 2, 1 in turn: the last one used goes first.
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
		}, []WindowID{2, 3, 1}},
		{"back to the oldest", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			focusNote(w, 1)
		}, []WindowID{1, 2, 3}},
		{"never focused follow by position", func(w *Workspace) {
			focusNote(w, 3)
		}, []WindowID{3, 1, 2}},
		{"closed windows are pruned", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			w.RemoveWindow(3)
		}, []WindowID{2, 1}},
		{"a moved column keeps its rank", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			w.MoveColumn(1)
			w.noteFocus()
		}, []WindowID{2, 3, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := threeColumns()
			tt.run(w)
			if got := w.switchOrder(); !slices.Equal(got, tt.want) {
				t.Fatalf("order %v, want %v", got, tt.want)
			}
			// recent never keeps a closed window nor two of one column.
			seen := map[int]bool{}
			for _, id := range w.recent {
				i := w.columnOf(id)
				if i < 0 || seen[i] {
					t.Fatalf("recent %v keeps window %d (column %d)", w.recent, id, i)
				}
				seen[i] = true
			}
		})
	}
}

func TestMRUOneWindowPerColumn(t *testing.T) {
	w := threeColumns()
	w.AddWindow(4)
	w.FocusColumn(-1)
	w.ConsumeOrExpel(-1)
	// Whatever the columns became, each appears once.
	for range 3 {
		w.noteFocus()
		w.FocusColumn(-1)
	}
	order := w.switchOrder()
	if len(order) != len(w.Columns) {
		t.Fatalf("order %v for %d columns", order, len(w.Columns))
	}
	seen := map[int]bool{}
	for _, id := range order {
		if seen[w.columnOf(id)] {
			t.Fatalf("order %v lists a column twice", order)
		}
		seen[w.columnOf(id)] = true
	}
}

func TestMRUSwitchOrderNeedsTwoColumns(t *testing.T) {
	w := workspace()
	if w.switchOrder() != nil {
		t.Fatal("order of an empty workspace")
	}
	w.AddWindow(1)
	w.noteFocus()
	if w.switchOrder() != nil {
		t.Fatal("order of one column")
	}
}

func TestMRUCapped(t *testing.T) {
	w := workspace()
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= switcherRecentMax+10; id++ {
		w.AddWindow(id)
		w.noteFocus()
	}
	if len(w.recent) != switcherRecentMax {
		t.Fatalf("recent holds %d", len(w.recent))
	}
}

func TestMRUNoteFocusDoesNotAllocateWhenSettled(t *testing.T) {
	w := threeColumns()
	focusNote(w, 2)
	if n := testing.AllocsPerRun(100, w.noteFocus); n != 0 {
		t.Fatalf("noteFocus allocates %v", n)
	}
	// A focus change inside the latest column updates its entry in place.
	w.AddWindow(4)
	w.ConsumeOrExpel(-1)
	w.FocusID(2)
	w.noteFocus()
	if len(w.recent) == 0 || w.recent[0] != 2 {
		t.Fatalf("recent %v, want window 2 first", w.recent)
	}
	n := len(w.recent)
	col := w.Columns[w.Focus]
	if len(col.Windows) < 2 {
		t.Fatalf("setup: column %v has one window", col.Windows)
	}
	other := col.Windows[0]
	if other == 2 {
		other = col.Windows[1]
	}
	w.FocusID(other)
	if got := testing.AllocsPerRun(100, w.noteFocus); got != 0 {
		t.Fatalf("noteFocus allocates %v after a same-column change", got)
	}
	if len(w.recent) != n || w.recent[0] != other {
		t.Fatalf("recent %v, want window %d first in place of 2 (%d entries)", w.recent, other, n)
	}
}

// Browsing the overview focuses nothing: the selection moving to another
// column, then Escape, leave the recent order as it was.
func TestOverviewBrowsingKeepsMRU(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	m := c.cur().mon
	w := m.Current()
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	indicatorScene(t, c)
	order, recent := w.switchOrder(), slices.Clone(w.recent)
	focus := switcherFocus(c)
	m.ToggleOverview()
	m.OverviewMove(1, 0)
	m.OverviewMove(1, 0)
	if switcherFocus(c) == focus {
		t.Fatal("setup: the overview selection is still on the focused window")
	}
	indicatorScene(t, c)
	// Cancelling would note the original column again: look while browsing.
	if !slices.Equal(w.recent, recent) {
		t.Fatalf("recent %v while browsing, was %v", w.recent, recent)
	}
	m.CancelOverview()
	indicatorScene(t, c)
	if got := w.switchOrder(); !slices.Equal(got, order) || !slices.Equal(w.recent, recent) {
		t.Fatalf("order %v recent %v, were %v %v", got, w.recent, order, recent)
	}
	if switcherFocus(c) != focus {
		t.Fatalf("focus %d, want %d", switcherFocus(c), focus)
	}
}

// switcherBinds returns the binds of the switcher tests: the defaults, a
// fullscreen bind to interrupt a switch and F9, a switch with no Cmd.
func switcherBinds() map[string]string {
	return map[string]string{
		"Cmd+Tab":       string(ActionSwitchColumnNext),
		"Cmd+Shift+Tab": string(ActionSwitchColumnPrev),
		"Cmd+f":         string(ActionToggleFullscreen),
		"F9":            string(ActionSwitchColumnNext),
	}
}

const (
	keyTab   = 15
	keySuper = 133
)

// switcherCore is indicatorCore with the switcher binds, up to three
// columns on screen and n one-window columns (windows 1..n, n focused),
// published once so the configures carry the windows' sizes.
func switcherCore(t *testing.T, n int) (*Core, *indicatorClock, <-chan ports.ClientCommand) {
	t.Helper()
	commands := make(chan ports.ClientCommand, 64)
	c, ic := indicatorCoreWith(t, commands)
	cfg := c.cfg
	cfg.Layout.MaxColumns = 3
	cfg.Binds = switcherBinds()
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	for id := WindowID(1); id <= WindowID(n); id++ {
		c.cur().mon.AddWindow(id)
	}
	indicatorScene(t, c)
	return c, ic, commands
}

// requireClosed fails unless no switcher is open: no monitor holds one, and
// the core has neither a switcher screen nor a timer.
func requireClosed(t *testing.T, c *Core) {
	t.Helper()
	for _, sc := range c.screens {
		if sc.mon.sw.ws != nil || sc.mon.sw.shown {
			t.Fatalf("screen %q still holds a switcher: %+v", sc.name(), sc.mon.sw)
		}
	}
	if c.switcher.sc != nil || c.switcher.timerC != nil || c.switcher.timerStop != nil {
		t.Fatalf("switcher screen %v, timer %v", c.switcher.sc != nil, c.switcher.timerC != nil)
	}
}

func tab(mods ports.Mods, pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: "Tab", Keycode: keyTab, Mods: mods, Pressed: pressed}
}

func shiftTab(pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: "ISO_Left_Tab", Base: "Tab", Keycode: keyTab, Mods: ports.ModSuper | ports.ModShift, Pressed: pressed}
}

func superKey(mods ports.Mods, pressed bool) ports.KeyEvent {
	return ports.KeyEvent{Keysym: "Super_L", Keycode: keySuper, Mods: mods, Pressed: pressed}
}

func bind(t *testing.T, c *Core, a Action) {
	t.Helper()
	if err := c.runBind(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func sendKeys(t *testing.T, c *Core, keys ...ports.KeyEvent) {
	t.Helper()
	for _, k := range keys {
		if err := c.keyEvent(context.Background(), k); err != nil {
			t.Fatal(err)
		}
	}
}

// lastScene returns the latest published scene.
func lastScene(t *testing.T, c *Core) ports.Scene {
	t.Helper()
	var s []ports.Scene
	for len(c.ch.Scenes) > 0 {
		s = <-c.ch.Scenes
	}
	if s == nil {
		t.Fatal("no scene published")
	}
	return s[0]
}

func switcherFocus(c *Core) WindowID {
	id, _ := c.cur().mon.Focused()
	return id
}

// forwarded drains the commands and returns the forwarded keys.
func forwarded(ch <-chan ports.ClientCommand) []ports.ForwardKey {
	var out []ports.ForwardKey
	for len(ch) > 0 {
		if v, ok := (<-ch).(ports.ForwardKey); ok {
			out = append(out, v)
		}
	}
	return out
}

func scenePreviews(s ports.Scene) []ports.SceneWindow {
	var out []ports.SceneWindow
	for _, w := range s.Windows {
		if w.Preview > 0 {
			out = append(out, w)
		}
	}
	return out
}

// --- P2.1: commit ---

func switcherMonitor(overflow Overflow, n int) (*Monitor, *Workspace) {
	m := NewMonitor()
	m.SetOutput(300, 200)
	m.SetMaxColumns(3)
	m.SetOverflow(overflow)
	for id := WindowID(1); id <= WindowID(n); id++ {
		m.AddWindow(id)
	}
	return m, m.Current()
}

func openSwitcher(m *Monitor, at int) {
	w := m.Current()
	m.sw = switcherState{ws: w, order: w.switchOrder(), at: at}
}

func TestSwitchCommitFocusesSelectedColumn(t *testing.T) {
	m, w := switcherMonitor(OverflowScroll, 3)
	openSwitcher(m, 1)
	want := m.sw.order[1]
	m.commitSwitcher()
	if id, _ := w.Focused(); id != want || m.sw.ws != nil {
		t.Fatalf("focus %d (want %d), switcher open %v", id, want, m.sw.ws != nil)
	}
}

func TestSwitchCommitTransfersMaximization(t *testing.T) {
	for _, overflow := range []Overflow{OverflowCascade, OverflowFixed, OverflowScroll} {
		t.Run(string(overflow), func(t *testing.T) {
			m, w := switcherMonitor(overflow, 2)
			a, b := w.Focus, 1-w.Focus
			w.FocusID(w.Columns[a].Windows[0])
			w.ToggleFullWidth()
			w.noteFocus()
			if !w.Columns[a].FullWidth {
				t.Fatal("setup: source not maximized")
			}
			openSwitcher(m, 1)
			m.commitSwitcher()
			if w.Focus != b || !w.Columns[b].FullWidth || w.Columns[a].FullWidth {
				t.Fatalf("focus %d, FullWidth source %v target %v", w.Focus, w.Columns[a].FullWidth, w.Columns[b].FullWidth)
			}
			if w.policy().equalCells && !w.hiddenByMaximized(a) {
				t.Fatal("source not hidden by the maximized target")
			}
		})
	}
}

func TestSwitchCommitLeavesPinnedFullscreen(t *testing.T) {
	m, w := switcherMonitor(OverflowFixed, 2)
	src := w.Columns[w.Focus].Windows[0]
	w.SetFullscreen(src, true)
	if w.fullscreen != src {
		t.Fatal("setup: no fullscreen")
	}
	openSwitcher(m, 1)
	target := m.sw.order[1]
	m.commitSwitcher()
	if w.fullscreen != 0 {
		t.Fatalf("fullscreen %d kept", w.fullscreen)
	}
	if id, _ := w.Focused(); id != target {
		t.Fatalf("focus %d, want %d", id, target)
	}
}

func TestSwitchCommitIgnoresGoneWindowAndOtherWorkspace(t *testing.T) {
	m, w := switcherMonitor(OverflowScroll, 3)
	before, _ := w.Focused()
	openSwitcher(m, 1)
	m.sw.order[1] = 99 // not in the workspace any more
	m.commitSwitcher()
	if id, _ := w.Focused(); id != before || m.sw.ws != nil {
		t.Fatalf("focus %d, want %d; open %v", id, before, m.sw.ws != nil)
	}
	openSwitcher(m, 1)
	m.sw.ws = &Workspace{}
	m.commitSwitcher()
	if id, _ := w.Focused(); id != before || m.sw.ws != nil {
		t.Fatalf("focus %d for a foreign workspace, want %d", id, before)
	}
}

// --- P2.2: actions and step ---

func TestSwitchStepOpensOnPreviousColumnAndCycles(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	c.mods = ports.ModSuper
	before := switcherFocus(c)
	if err := c.runBind(context.Background(), ActionSwitchColumnNext); err != nil {
		t.Fatal(err)
	}
	sw := &c.cur().mon.sw
	if sw.ws == nil || sw.at != 1 || switcherFocus(c) != before {
		t.Fatalf("open %v at %d focus %d (was %d)", sw.ws != nil, sw.at, switcherFocus(c), before)
	}
	if len(ic.timers) != 1 || ic.timers[0] != switcherDelay {
		t.Fatalf("timers %v, want one of %v", ic.timers, switcherDelay)
	}
	bind(t, c, ActionSwitchColumnNext)
	if sw.at != 2 {
		t.Fatalf("at %d", sw.at)
	}
	bind(t, c, ActionSwitchColumnNext)
	if sw.at != 0 {
		t.Fatalf("at %d, want wrap to 0", sw.at)
	}
	bind(t, c, ActionSwitchColumnPrev)
	if sw.at != 2 {
		t.Fatalf("at %d, want wrap to 2", sw.at)
	}
	bind(t, c, ActionSwitchColumnPrev)
	if sw.at != 1 {
		t.Fatalf("at %d", sw.at)
	}
	if len(ic.timers) != 1 {
		t.Fatalf("timers %v: stepping must not re-arm", ic.timers)
	}
}

func TestSwitchWithoutCmdCommitsAtOnce(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	// F9 is bound to switch-column-next with no Cmd held.
	f9 := func(pressed bool) ports.KeyEvent {
		return ports.KeyEvent{Keysym: "F9", Keycode: 75, Pressed: pressed}
	}
	sendKeys(t, c, f9(true), f9(false))
	requireClosed(t, c)
	if len(ic.timers) != 0 {
		t.Fatalf("timers %v armed for a switch with no Cmd", ic.timers)
	}
	if switcherFocus(c) == start {
		t.Fatal("focus did not move")
	}
	sendKeys(t, c, f9(true), f9(false))
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want back on %d", switcherFocus(c), start)
	}
}

func TestSwitchIgnoredWithOneColumnOrOverview(t *testing.T) {
	c, _, _ := switcherCore(t, 1)
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	requireClosed(t, c)
	c, _, _ = switcherCore(t, 3)
	c.cur().mon.ToggleOverview()
	before := switcherFocus(c)
	bind(t, c, ActionSwitchColumnNext)
	requireClosed(t, c)
	if switcherFocus(c) != before {
		t.Fatal("switcher acted in the overview")
	}
}

// --- P2.3: release commit and timer ---

func TestSwitchQuickTapSwapsWithoutCards(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	for len(c.ch.Scenes) > 0 {
		<-c.ch.Scenes
	}
	tap := func() {
		t.Helper()
		sendKeys(t, c,
			superKey(ports.ModSuper, true),
			tab(ports.ModSuper, true),
			tab(ports.ModSuper, false),
			superKey(0, false),
		)
		for len(c.ch.Scenes) > 0 {
			if p := scenePreviews((<-c.ch.Scenes)[0]); len(p) > 0 {
				t.Fatalf("cards published during a quick tap: %+v", p)
			}
		}
		requireClosed(t, c)
	}
	tap()
	if switcherFocus(c) == start {
		t.Fatal("tap did not swap")
	}
	tap()
	if switcherFocus(c) != start {
		t.Fatalf("second tap focus %d, want back on %d", switcherFocus(c), start)
	}
}

func TestSwitchHoldCommitsOnReleaseAndForwardsIt(t *testing.T) {
	c, _, cmds := switcherCore(t, 3)
	start := switcherFocus(c)
	order := c.cur().mon.Current().switchOrder()
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true), tab(ports.ModSuper, false))
	if switcherFocus(c) != start || c.cur().mon.sw.ws == nil {
		t.Fatal("focus moved or switcher closed before the release")
	}
	forwarded(cmds)
	sendKeys(t, c, superKey(0, false))
	requireClosed(t, c)
	if switcherFocus(c) != order[1] {
		t.Fatalf("focus %d, want %d", switcherFocus(c), order[1])
	}
	fk := forwarded(cmds)
	if len(fk) != 1 || fk[0].Key.Keysym != "Super_L" || fk[0].Key.Pressed || fk[0].ID != order[1] {
		t.Fatalf("release not forwarded to the new focus: %+v", fk)
	}
}

// --- P3.1: keys and gestures ---

func TestSwitchEscapeCancelsAndIsNotForwarded(t *testing.T) {
	c, _, cmds := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true), tab(ports.ModSuper, false))
	forwarded(cmds)
	esc := ports.KeyEvent{Keysym: "Escape", Keycode: 1, Mods: ports.ModSuper, Pressed: true}
	sendKeys(t, c, esc)
	esc.Pressed = false
	sendKeys(t, c, esc, superKey(0, false))
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want %d", switcherFocus(c), start)
	}
	for _, k := range forwarded(cmds) {
		if k.Key.Keysym == "Escape" {
			t.Fatalf("Escape forwarded: %+v", k)
		}
	}
}

func TestSwitchAnotherBindCancelsThenRuns(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	sendKeys(t, c, ports.KeyEvent{Keysym: "f", Keycode: 33, Mods: ports.ModSuper, Pressed: true})
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d moved", switcherFocus(c))
	}
	if c.cur().mon.Current().fullscreen != start {
		t.Fatalf("bind not applied: fullscreen %d", c.cur().mon.Current().fullscreen)
	}
}

func TestSwitchShiftTabGoesBackward(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), shiftTab(true))
	sw := c.cur().mon.sw
	if sw.ws == nil || sw.at != len(sw.order)-1 {
		t.Fatalf("open %v at %d of %d", sw.ws != nil, sw.at, len(sw.order))
	}
	sendKeys(t, c, shiftTab(false), tab(ports.ModSuper, true))
	if c.cur().mon.sw.at != 0 {
		t.Fatalf("at %d, want 0 after next", c.cur().mon.sw.at)
	}
}

func TestSwitchSwipeBeginCancels(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want %d", switcherFocus(c), start)
	}
}

func TestSwitchSecurityChangeCancels(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	state := ports.SecurityState{Generation: 1}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state }).Maybe()
	c.opts.Security = gate
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	state = ports.SecurityState{Generation: 2}
	if !c.syncSecurity() {
		t.Fatal("security change not seen")
	}
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want %d", switcherFocus(c), start)
	}
}

func TestSwitchCmdKeyFollowsConfig(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	cfg := c.cfg
	cfg.Keyboard.CmdKey = "alt"
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	order := c.cur().mon.Current().switchOrder()
	sendKeys(t, c, tab(ports.ModAlt, true))
	if c.cur().mon.sw.ws == nil {
		t.Fatal("Alt+Tab did not open the switcher")
	}
	sendKeys(t, c, ports.KeyEvent{Keysym: "Alt_L", Keycode: 64, Pressed: false})
	requireClosed(t, c)
	if switcherFocus(c) != order[1] {
		t.Fatalf("focus %d, want %d", switcherFocus(c), order[1])
	}
}

// --- P3.2: layout ---

func TestSwitcherLayoutCards(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	c.cfg.Floating.Dim = 0.4
	// A wider focused column: the cards have different aspect ratios.
	c.cur().mon.Current().ResizeColumn(30)
	indicatorScene(t, c)
	area := c.cur().mon.Current().overviewArea()
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	if s := indicatorScene(t, c); len(scenePreviews(s)) != 0 || c.cur().mon.sw.shown {
		t.Fatalf("cards before the delay: %+v", s.Windows)
	}
	order := c.cur().mon.sw.order
	if !c.switcherTick() || !c.cur().mon.sw.shown || c.switcher.timerC != nil {
		t.Fatal("the tick did not show the cards and clear the timer")
	}
	s := indicatorScene(t, c)
	cards := scenePreviews(s)
	if len(cards) != 3 {
		t.Fatalf("%d cards: %+v", len(cards), s.Windows)
	}
	focused := 0
	distinct := false
	var centers []int
	for k, card := range cards {
		if card.ID != order[k] {
			t.Fatalf("card %d is window %d, want %d (MRU order)", k, card.ID, order[k])
		}
		// The boxes are equal: the cards' centres are evenly spaced.
		centers = append(centers, card.Rect.X+card.Rect.W/2)
		// A card is its window's client size, scaled down uniformly.
		sent := c.configures.sent[card.ID]
		if sent.Width <= 0 || sent.Height <= 0 || card.Preview <= 0 || card.Preview >= 1 {
			t.Fatalf("card %d: client %dx%d, preview %v", card.ID, sent.Width, sent.Height, card.Preview)
		}
		if d := card.Rect.W - int(math.Round(float64(sent.Width)*card.Preview)); d < -1 || d > 1 {
			t.Fatalf("card %d is %dx%d for a %dx%d client at %v", card.ID, card.Rect.W, card.Rect.H, sent.Width, sent.Height, card.Preview)
		}
		if d := card.Rect.H - int(math.Round(float64(sent.Height)*card.Preview)); d < -1 || d > 1 {
			t.Fatalf("card %d is %dx%d for a %dx%d client at %v", card.ID, card.Rect.W, card.Rect.H, sent.Width, sent.Height, card.Preview)
		}
		if first := c.configures.sent[order[0]]; sent.Width != first.Width {
			distinct = true
		}
		if card.Focused {
			focused++
			if card.ID != order[c.cur().mon.sw.at] {
				t.Fatalf("focused card %d is not the selected %d", card.ID, order[c.cur().mon.sw.at])
			}
		}
	}
	if !distinct {
		t.Fatal("setup: every window has the same size, so the aspect ratios prove nothing")
	}
	if d := (centers[1] - centers[0]) - (centers[2] - centers[1]); d < -2 || d > 2 {
		t.Fatalf("boxes not equal: card centres %v", centers)
	}
	if d := centers[0] + centers[2] - 2*(area.X+area.W/2); d < -2 || d > 2 {
		t.Fatalf("row not centered in %+v: card centres %v", area, centers)
	}
	if focused != 1 {
		t.Fatalf("%d focused cards", focused)
	}
	if len(s.Separators) != 4 {
		t.Fatalf("separators %+v, want the 4 lines of the outline", s.Separators)
	}
	if s.Dim != 0.4 {
		t.Fatalf("dim %v", s.Dim)
	}
	if s.TileInset != (ports.Insets{}) {
		t.Fatalf("tile inset %+v", s.TileInset)
	}
	for _, w := range s.Windows {
		if w.Preview == 0 && !w.Hidden {
			t.Fatalf("window %d drawn under the cards", w.ID)
		}
	}
	// A commit gives the workspace back.
	sendKeys(t, c, superKey(0, false))
	if s := lastScene(t, c); len(scenePreviews(s)) != 0 {
		t.Fatalf("cards after the commit: %+v", s.Windows)
	}
}

func TestSwitcherLayoutHidesOtherWindowsOfColumns(t *testing.T) {
	// Windows 10 and 11 are on a second workspace.
	c, _ := twoWorkspaces(t)
	m := c.cur().mon
	m.AddWindow(4)
	w := m.Current()
	w.ConsumeOrExpel(-1)
	m.AddFloating(9, 100, 80)
	if len(w.Columns) != 3 || w.columnWindows() != 4 || len(w.Floats) != 1 {
		t.Fatalf("setup: %d columns, %d windows, %d floats", len(w.Columns), w.columnWindows(), len(w.Floats))
	}
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	s := indicatorScene(t, c)
	reps := map[WindowID]bool{}
	for _, id := range m.sw.order {
		reps[id] = true
	}
	seen := map[WindowID]bool{}
	for _, sw := range s.Windows {
		seen[sw.ID] = true
		if reps[sw.ID] {
			if sw.Preview == 0 || sw.Hidden {
				t.Fatalf("representative %d not a card: %+v", sw.ID, sw)
			}
		} else if !sw.Hidden {
			t.Fatalf("window %d not hidden: %+v", sw.ID, sw)
		}
	}
	// The other window of a column, the float and the other workspace's
	// windows are all in the scene, hidden.
	for _, id := range []WindowID{4, 9, 10, 11} {
		if !seen[id] {
			t.Fatalf("window %d missing from the scene (hidden windows must be listed): %+v", id, s.Windows)
		}
	}
}

// While the cards show, the screen draws previews: the tiles do not stop at
// the panels, a card takes no pointer, nothing is
// dragged, and a window mapping or unmapping behind them does not animate.
func TestSwitcherShownIsAPreview(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	c.cfg.Animations.On = true
	m := c.cur().mon
	// A bar reserves the top of the output: tiles stop below it.
	c.setLayers([]ports.LayerSurface{{ID: 90, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Width: 300, Height: 20, ExclusiveZone: 20}})
	if s := indicatorScene(t, c); s.TileInset.Top != 20 {
		t.Fatalf("setup: tile inset %+v, want the bar's 20", s.TileInset)
	}
	sizes := map[WindowID]ports.ConfigureWindow{}
	for id, v := range c.configures.sent {
		sizes[id] = v
	}
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	// The cards fly in, and the motions survive a publish.
	if len(c.cur().rects) == 0 {
		t.Fatal("no card motion after the show")
	}
	settle := func() {
		t.Helper()
		for range 200 {
			ic.now = ic.now.Add(16 * time.Millisecond)
			if err := c.step(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
		}
		lastScene(t, c)
		if len(c.cur().rects) != 0 || c.animating() {
			t.Fatalf("motions did not settle: %d rects", len(c.cur().rects))
		}
	}
	settle()
	if !m.switcherShown() {
		t.Fatal("setup: cards not shown")
	}
	s := indicatorScene(t, c)
	if len(scenePreviews(s)) != 3 {
		t.Fatalf("%d cards", len(scenePreviews(s)))
	}
	// A card keeps its client's real size.
	for _, id := range m.sw.order {
		if got := c.configures.sent[id]; got.Width != sizes[id].Width || got.Height != sizes[id].Height {
			t.Fatalf("window %d configured %dx%d under its card, was %dx%d", id, got.Width, got.Height, sizes[id].Width, sizes[id].Height)
		}
	}
	if s.TileInset != (ports.Insets{}) {
		t.Fatalf("tile inset %+v while the cards show", s.TileInset)
	}
	// The pointer finds no window under a card.
	card := m.sw.order[1]
	x, y := cardCenter(t, c, card)
	if id, _, _ := c.hit(x, y); id != 0 {
		t.Fatalf("hit window %d under a card", id)
	}
	if _, _, ok := c.draggable(card); ok {
		t.Fatal("a card can be dragged")
	}
	if tgt := c.dropAt(card, x, y); tgt.kind != dropNone {
		t.Fatalf("a drop target %+v under the cards", tgt)
	}
	// A map and an unmap behind the cards do not animate.
	c.mapWindow(ports.WindowMapped{ID: 20})
	if len(c.cur().rects) != 0 {
		t.Fatalf("a map behind the cards started motions: %+v", c.cur().rects)
	}
	gone := m.sw.order[2]
	c.unmapWindow(ports.WindowUnmapped{ID: gone})
	if len(c.cur().rects) != 0 {
		t.Fatalf("an unmap behind the cards started motions: %+v", c.cur().rects)
	}
	if m.Current().columnOf(gone) >= 0 || len(m.sw.order) != 2 {
		t.Fatalf("window %d not removed from the cards: %v", gone, m.sw.order)
	}
	// The workspace comes back with the panel inset.
	sendKeys(t, c, superKey(0, false))
	settle()
	if s := indicatorScene(t, c); s.TileInset.Top != 20 || len(scenePreviews(s)) != 0 {
		t.Fatalf("after the commit: tile inset %+v, %d cards", s.TileInset, len(scenePreviews(s)))
	}
}

// The predicates that choose between the workspace and its previews follow
// the cards as they follow the overview: the frame, a covering fullscreen
// and the capture state.
func TestSwitcherShownPreviewPredicates(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	sc := c.cur()
	m := sc.mon
	w := m.Current()
	w.Overflow = OverflowFixed // a fullscreen window covers whatever the scroll
	w.SetSize(100, 100)        // the workspace frame is smaller than the output
	tile := w.Columns[w.Focus].Windows[0]
	w.SetFullscreen(tile, true)
	indicatorScene(t, c)
	c.capt.sessions = []*capSession{{open: ports.CaptureSessionOpen{ID: 1, Workspace: w.ID}}}
	type state struct {
		frameIsOutput, covers, captureHidden bool
		captureShown                         uint64
	}
	read := func() state {
		r, reason := c.capResolve(capTarget{workspace: w.ID})
		if reason != ports.CaptureReasonNone {
			t.Fatalf("capture target: %v", reason)
		}
		var shown uint64
		if cs := c.captureSceneFor(sc, &capView{}); cs != nil {
			shown = cs.Shown
		}
		return state{m.Frame() == m.Output(), hasFullscreen(sc), r.hidden, shown}
	}
	if got, want := read(), (state{false, true, false, w.ID}); got != want {
		t.Fatalf("closed: %+v, want %+v", got, want)
	}
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	c.switcherTick()
	if !m.switcherShown() {
		t.Fatal("setup: cards not shown")
	}
	if got, want := read(), (state{true, false, true, 0}); got != want {
		t.Fatalf("shown: %+v, want %+v", got, want)
	}
}

// A card's window with no configure sent yet (it mapped since the last
// publish) is sized from its real client rect, not from the card.
func TestSwitcherCardOfUnconfiguredWindowKeepsRealSize(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	m := c.cur().mon
	id := m.Current().switchOrder()[1]
	c.configures.forget(id) // the card falls back to the usable size
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	c.configures.forget(id) // the bind's publish configured it again
	c.switcherTick()
	indicatorScene(t, c)
	var real Placement
	for _, p := range m.Current().Layout() {
		if p.ID == id {
			real = p
		}
	}
	want := c.clientRect(real)
	if got := c.configures.sent[id]; got.Width != want.W || got.Height != want.H || want.W == 0 {
		t.Fatalf("window %d configured %dx%d under its card, real client %dx%d", id, got.Width, got.Height, want.W, want.H)
	}
}

// A focus change behind the cards (the real focus, not the selection) is not
// recorded as a use of the column until the cards are gone.
func TestSwitcherShownDoesNotNoteFocus(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	w := c.cur().mon.Current()
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	c.switcherTick()
	before := slices.Clone(w.recent)
	w.FocusID(c.cur().mon.sw.order[2])
	indicatorScene(t, c)
	if !slices.Equal(w.recent, before) {
		t.Fatalf("recent %v, was %v: noted while the cards show", w.recent, before)
	}
}

// Showing the cards drops the motions of windows they hide.
func TestSwitcherShowDropsMotionsOfHiddenWindows(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 4})
	w := sc.mon.Current()
	w.ConsumeOrExpel(-1) // 4 joins 3's column
	w.FocusID(3)         // 3 stands for the column; 4 is hidden
	if _, ok := sc.rects[4]; !ok {
		t.Fatal("setup: no entrance motion for window 4")
	}
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	if _, ok := sc.rects[4]; !ok {
		t.Fatal("setup: motion dropped by the bind")
	}
	c.switcherTick()
	if _, ok := sc.rects[4]; ok {
		t.Fatalf("a hidden window keeps its motion: %+v", sc.rects[4])
	}
}

func TestSwitcherCardsKeepKeysOnRealFocus(t *testing.T) {
	c, _, cmds := switcherCore(t, 3)
	focus := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	forwarded(cmds)
	if c.keyboardFocus() != focus {
		t.Fatalf("keyboard focus %d, want %d", c.keyboardFocus(), focus)
	}
	sendKeys(t, c, ports.KeyEvent{Keysym: "x", Keycode: 45, Mods: ports.ModSuper, Pressed: true})
	// An unbound key goes to the real focus (Cmd+x has no bind here).
	fk := forwarded(cmds)
	if len(fk) != 1 || fk[0].ID != focus || fk[0].Key.Keysym != "x" {
		t.Fatalf("forwarded %+v, want x to %d", fk, focus)
	}
}

// --- P3.3: click ---

func cardCenter(t *testing.T, c *Core, id WindowID) (float64, float64) {
	t.Helper()
	for _, p := range c.cur().shownLayout() {
		if p.ID == id && p.Preview > 0 {
			return float64(p.Rect.X + p.Rect.W/2), float64(p.Rect.Y + p.Rect.H/2)
		}
	}
	t.Fatalf("no card for %d", id)
	return 0, 0
}

func TestSwitcherClickOnCardCommits(t *testing.T) {
	c, _, cmds := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	target := c.cur().mon.sw.order[2]
	c.cursorX, c.cursorY = cardCenter(t, c, target)
	forwarded(cmds)
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, c)
	if switcherFocus(c) != target {
		t.Fatalf("focus %d, want %d", switcherFocus(c), target)
	}
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110}); err != nil {
		t.Fatal(err)
	}
	for len(cmds) > 0 {
		if v, ok := (<-cmds).(ports.PointerButtonTo); ok {
			t.Fatalf("click reached a client: %+v", v)
		}
	}
}

// A click lands on a card where it is drawn, while its motion still runs.
func TestSwitcherClickOnMovingCardCommits(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	c.cfg.Animations.On = true
	sc := c.cur()
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	ic.now = ic.now.Add(16 * time.Millisecond)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	lastScene(t, c)
	target := sc.mon.sw.order[2]
	if len(sc.rects) == 0 || !c.animating() {
		t.Fatalf("setup: %d card motions, animating %v", len(sc.rects), c.animating())
	}
	var settled, shown Rect
	for _, p := range sc.settledLayout {
		if p.ID == target {
			settled = p.Rect
		}
	}
	for _, p := range sc.shownLayout() {
		if p.ID == target {
			shown = p.Rect
		}
	}
	if settled == shown {
		t.Fatalf("setup: card %d shown at its settled rect %+v", target, shown)
	}
	c.cursorX, c.cursorY = cardCenter(t, c, target)
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, c)
	if switcherFocus(c) != target {
		t.Fatalf("focus %d, want %d", switcherFocus(c), target)
	}
}

// Cancelling while the switch is held leaves nothing for the delayed show.
func TestSwitcherTickAfterCancelDoesNothing(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	if !c.switching() {
		t.Fatal("setup: no switcher open")
	}
	c.cancelSwitcher()
	if c.switcherTick() {
		t.Fatal("switcherTick reports a switcher after the cancel")
	}
	requireClosed(t, c)
}

func TestSwitcherClickElsewhereCancels(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	c.cursorX, c.cursorY = 1, 1
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want %d", switcherFocus(c), start)
	}
}

func TestSwitcherPressBeforeCardsCancelsAndContinues(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	handled, err := c.switcherClick(context.Background(), 0x110)
	if err != nil || handled {
		t.Fatalf("handled %v err %v", handled, err)
	}
	requireClosed(t, c)
	if switcherFocus(c) != start {
		t.Fatal("press did not cancel")
	}
}

// --- P3.4: robustness ---

func TestSwitcherClosingSelectedWindowKeepsSelectionValid(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	sw := &c.cur().mon.sw
	gone := sw.order[sw.at]
	c.cur().mon.RemoveWindow(gone)
	if sw.ws == nil || len(sw.order) != 2 || sw.at < 0 || sw.at >= len(sw.order) || slices.Contains(sw.order, gone) {
		t.Fatalf("open %v order %v at %d", sw.ws != nil, sw.order, sw.at)
	}
	s := indicatorScene(t, c)
	if n := len(scenePreviews(s)); n != 2 {
		t.Fatalf("%d cards after a close", n)
	}
	want := sw.order[sw.at]
	sendKeys(t, c, superKey(0, false))
	if switcherFocus(c) != want {
		t.Fatalf("focus %d, want neighbour %d", switcherFocus(c), want)
	}
}

// Closing a window ahead of the selection shifts the cards left: the
// selection follows its card, so it still stands on the same window.
func TestSwitcherClosingBeforeSelectionKeepsCard(t *testing.T) {
	c, _, _ := switcherCore(t, 4)
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	sw := &c.cur().mon.sw
	if sw.at != 1 || len(sw.order) != 4 {
		t.Fatalf("setup: at %d of %d", sw.at, len(sw.order))
	}
	selected := sw.order[sw.at]
	c.cur().mon.RemoveWindow(sw.order[0])
	if sw.ws == nil || len(sw.order) != 3 {
		t.Fatalf("order %v after the close", sw.order)
	}
	if sw.at != 0 || sw.order[sw.at] != selected {
		t.Fatalf("selection moved from %d: order %v at %d, want at 0", selected, sw.order, sw.at)
	}
	sendKeys(t, c, superKey(0, false))
	if switcherFocus(c) != selected {
		t.Fatalf("focus %d after the commit, want %d", switcherFocus(c), selected)
	}
}

func TestSwitcherClosingDownToOneColumnClosesIt(t *testing.T) {
	c, _, _ := switcherCore(t, 2)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	c.cur().mon.RemoveWindow(c.cur().mon.sw.order[1])
	if sw := c.cur().mon.sw; sw.ws != nil || sw.shown {
		t.Fatal("switcher still open with one column")
	}
	if s := indicatorScene(t, c); len(scenePreviews(s)) != 0 {
		t.Fatalf("cards left: %+v", s.Windows)
	}
	// The timer fires on a closed switcher: harmless, and it clears the
	// core's side.
	if c.switcherTick() {
		t.Fatal("tick on a closed switcher")
	}
	requireClosed(t, c)
	sendKeys(t, c, superKey(0, false))
}

func TestSwitcherOutputRemovalCancels(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	sc := c.switcher.sc
	if sc == nil {
		t.Fatal("no switcher screen")
	}
	c.removeScreen(sc.name())
	requireClosed(t, c)
	if sc.mon.sw.ws != nil {
		t.Fatal("switcher survived its output")
	}
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.switcherTick() {
		t.Fatal("tick after the output went")
	}
}

func TestSwitcherFocusMovingToAnotherOutputReopensThere(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	first := c.cur()
	for id := WindowID(11); id <= 13; id++ {
		c.screens[c.screenIndex("B")].mon.AddWindow(id)
	}
	indicatorScene(t, c)
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	if c.switcher.sc != first {
		t.Fatal("switcher not on the focused screen")
	}
	c.focusScreen = c.screenIndex("B")
	bind(t, c, ActionSwitchColumnNext)
	if first.mon.sw.ws != nil {
		t.Fatal("old switcher left open")
	}
	if c.switcher.sc != c.cur() || c.cur().mon.sw.ws == nil || c.cur().mon.sw.at != 1 {
		t.Fatalf("new switcher on %v open %v", c.switcher.sc == c.cur(), c.cur().mon.sw.ws != nil)
	}
}

// With animations on, the cards fly in and the commit flies them back; the
// windows end where the layout puts them, with no card left.
func TestSwitcherAnimatedHoldAndCommit(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	c.cfg.Animations.On = true
	target := c.cur().mon.Current().switchOrder()[1]
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(scenePreviews(lastScene(t, c))) != 3 {
		t.Fatal("cards not shown")
	}
	sendKeys(t, c, superKey(0, false))
	for range 200 {
		ic.now = ic.now.Add(16 * time.Millisecond)
		if err := c.step(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	s := lastScene(t, c)
	if len(scenePreviews(s)) != 0 || switcherFocus(c) != target {
		t.Fatalf("cards %d, focus %d (want %d)", len(scenePreviews(s)), switcherFocus(c), target)
	}
	if n := len(c.cur().rects); n != 0 || c.animating() {
		t.Fatalf("motions left: %d rect motions, animating %v", n, c.animating())
	}
}

// --- review fixes ---

// twoWorkspaces is a switcher core whose second workspace holds two
// windows (10, 11) and is not on screen.
func twoWorkspaces(t *testing.T) (*Core, *Workspace) {
	t.Helper()
	c, _, _ := switcherCore(t, 3)
	c.cfg.Floating.Dim = 0.4
	m := c.cur().mon
	first := m.Current()
	m.Focus(1)
	m.AddWindow(10)
	m.AddWindow(11)
	other := m.Current()
	m.show(first)
	if other == first || m.Current() != first {
		t.Fatal("workspaces not set up")
	}
	indicatorScene(t, c)
	return c, other
}

func TestSwitcherShownDropsWhenAnotherWorkspaceComesOnScreen(t *testing.T) {
	c, other := twoWorkspaces(t)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	if s := indicatorScene(t, c); len(scenePreviews(s)) != 3 {
		t.Fatalf("cards not shown: %+v", s.Windows)
	}
	// An xdg-activation request for a window of the other workspace.
	if err := c.activate(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	m := c.cur().mon
	if m.Current() != other || m.previewing() {
		t.Fatalf("on screen %v, previewing %v", m.Current() == other, m.previewing())
	}
	s := indicatorScene(t, c)
	if len(scenePreviews(s)) != 0 || s.Dim != 0 || s.DimBehind {
		t.Fatalf("scene still dimmed or previewing: dim %v previews %+v", s.Dim, scenePreviews(s))
	}
	requireClosed(t, c)
	var any bool
	for _, p := range c.cur().shownLayout() {
		if p.ID == 10 || p.ID == 11 {
			x, y := float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2)
			if id, _, _ := c.hit(x, y); id != p.ID {
				t.Fatalf("hit %d at window %d", id, p.ID)
			}
			any = true
		}
	}
	if !any {
		t.Fatal("workspace windows not laid out")
	}
	// The next switch opens fresh on the new workspace.
	c.mods = ports.ModSuper
	bind(t, c, ActionSwitchColumnNext)
	if sw := m.sw; sw.ws == nil || sw.ws != other || sw.at != 1 {
		t.Fatalf("open %v on other %v at %d", sw.ws != nil, sw.ws == other, sw.at)
	}
}

func TestSwitcherStepOnStaleWorkspaceOpensFresh(t *testing.T) {
	c, other := twoWorkspaces(t)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	// Shown on screen, then the workspace changes with no publish between.
	c.cur().mon.show(other)
	if c.cur().mon.previewing() {
		t.Fatal("stale cards still count as previews")
	}
	sendKeys(t, c, tab(ports.ModSuper, true))
	sw := c.cur().mon.sw
	if sw.ws == nil || sw.ws != other || sw.shown || sw.at != 1 {
		t.Fatalf("open %v on other %v shown %v at %d", sw.ws != nil, sw.ws == other, sw.shown, sw.at)
	}
	// A release on a stale switcher commits nothing and closes it.
	c.cur().mon.show(c.cur().mon.Workspaces[0])
	focus := switcherFocus(c)
	sendKeys(t, c, superKey(0, false))
	requireClosed(t, c)
	if switcherFocus(c) != focus {
		t.Fatal("stale switcher survived the release")
	}
}

// The delay passes after another workspace came on screen with no publish
// in between: the tick drops the switcher instead of showing stale cards.
func TestSwitcherTickOnStaleWorkspaceCancels(t *testing.T) {
	c, other := twoWorkspaces(t)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	m := c.cur().mon
	if m.sw.ws == nil {
		t.Fatal("switcher not open")
	}
	m.show(other)
	if !c.switcherTick() {
		t.Fatal("tick on a stale workspace asks for no scene")
	}
	requireClosed(t, c)
}

func TestSwitchIgnoredDuringDrag(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	d := &dragState{id: start, button: btnLeft}
	c.drag = d
	c.buttons[btnLeft] = true
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	requireClosed(t, c)
	if c.drag != d || switcherFocus(c) != start {
		t.Fatal("drag disturbed")
	}
	sendKeys(t, c, ports.KeyEvent{Keysym: "Escape", Keycode: 1, Mods: ports.ModSuper, Pressed: true})
	if c.drag != nil {
		t.Fatal("Escape did not cancel the drag")
	}
}

// Another bind closing the shown cards still flies them back: the snapshot
// is taken while they are drawn, so the windows move from the card rects.
func TestSwitchBindOverShownCardsFliesBack(t *testing.T) {
	c, ic, _ := switcherCore(t, 3)
	c.cfg.Animations.On = true
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	for range 200 {
		ic.now = ic.now.Add(16 * time.Millisecond)
		if err := c.step(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	sc := c.cur()
	cards := map[WindowID]Rect{}
	for _, p := range sc.shownLayout() {
		if p.Preview > 0 {
			cards[p.ID] = p.Rect
		}
	}
	if len(cards) != 3 || len(sc.rects) != 0 {
		t.Fatalf("%d cards, %d motions: the cards did not settle", len(cards), len(sc.rects))
	}
	bind(t, c, ActionFocusColumnLeft)
	if c.switching() || sc.mon.sw.shown {
		t.Fatal("the bind left the switcher open")
	}
	settled := map[WindowID]Rect{}
	for _, p := range sc.mon.Layout() {
		settled[p.ID] = p.Rect
	}
	for id, card := range cards {
		rm, ok := sc.rects[id]
		if !ok || !rm.scale {
			t.Fatalf("window %d has no fly-back motion", id)
		}
		if got := rm.apply(settled[id]); got != card {
			t.Fatalf("window %d flies back from %+v, want its card %+v", id, got, card)
		}
	}
}

func TestSwitcherClosingSelectedWindowPicksColumnsNextWindow(t *testing.T) {
	m := NewMonitor()
	m.SetOutput(300, 200)
	m.SetMaxColumns(3)
	for id := WindowID(1); id <= 4; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	w.FocusID(3)
	w.ConsumeOrExpel(-1) // columns [1] [2 3] [4]
	if len(w.Columns) != 3 || len(w.Columns[1].Windows) != 2 {
		t.Fatalf("columns %+v", w.Columns)
	}
	w.FocusID(1)
	openSwitcher(m, 0)
	at := slices.IndexFunc(m.sw.order, func(id WindowID) bool { return w.columnOf(id) == 1 })
	m.sw.at = at
	gone := m.sw.order[at]
	size := Rect{W: 120, H: 90}
	m.sw.sizes = map[WindowID]Rect{gone: size}
	other := w.Columns[1].Windows[0]
	if other == gone {
		other = w.Columns[1].Windows[1]
	}
	m.RemoveWindow(gone)
	if len(m.sw.order) != 3 || m.sw.at != at || m.sw.order[at] != other {
		t.Fatalf("order %v at %d, want card %d to stand for %d", m.sw.order, m.sw.at, at, other)
	}
	if m.sw.sizes[other] != size {
		t.Fatalf("sizes %v: the new card keeps no size of the closed window's", m.sw.sizes)
	}
	m.commitSwitcher()
	if id, _ := w.Focused(); id != other {
		t.Fatalf("focus %d, want %d", id, other)
	}
}

func TestSwitchCommitFocusesTileUnderFloat(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	m := c.cur().mon
	m.AddFloating(9, 100, 80)
	w := m.Current()
	if !w.floatFocus {
		t.Fatal("float not focused")
	}
	tile := w.Columns[w.Focus].Windows[0]
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	bind(t, c, ActionSwitchColumnPrev) // back to card 0: the column under the float
	if m.sw.at != 0 || m.sw.order[0] != tile {
		t.Fatalf("at %d order %v", m.sw.at, m.sw.order)
	}
	sendKeys(t, c, superKey(0, false))
	if w.floatFocus || switcherFocus(c) != tile {
		t.Fatalf("float focused %v, focus %d, want tile %d", w.floatFocus, switcherFocus(c), tile)
	}
}

func TestSwitchCommitFollowsToItsOutput(t *testing.T) {
	c, _, _ := switcherCore(t, 3)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	first := c.focusScreen
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	want := c.cur().mon.sw.order[1]
	// The pointer crosses to the other output meanwhile.
	c.focusScreen = c.screenIndex("B")
	sendKeys(t, c, superKey(0, false))
	if c.focusScreen != first || switcherFocus(c) != want {
		t.Fatalf("focus screen %d (want %d), focus %d (want %d)", c.focusScreen, first, switcherFocus(c), want)
	}
}
