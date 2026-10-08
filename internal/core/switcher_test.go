package core

import (
	"context"
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

func TestMRUOrderFollowsFocus(t *testing.T) {
	w := threeColumns()
	// Columns 0, 2, 1 in turn: the last one used goes first.
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	if got, want := w.switchOrder(), []WindowID{2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	focusNote(w, 1)
	if got, want := w.switchOrder(), []WindowID{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestMRUColumnsNeverFocusedFollowByPosition(t *testing.T) {
	w := threeColumns()
	focusNote(w, 3)
	if got, want := w.switchOrder(), []WindowID{3, 1, 2}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestMRUPrunesClosedWindows(t *testing.T) {
	w := threeColumns()
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	w.RemoveWindow(3)
	if got, want := w.switchOrder(), []WindowID{2, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if slices.Contains(w.recent, 3) {
		t.Fatalf("recent %v keeps a closed window", w.recent)
	}
}

func TestMRUKeepsRankWhenColumnMoves(t *testing.T) {
	w := threeColumns()
	focusNote(w, 1)
	focusNote(w, 3)
	focusNote(w, 2)
	// Window 2 moves to the right: its column is the same, so is its rank.
	w.MoveColumn(1)
	w.noteFocus()
	if got, want := w.switchOrder(), []WindowID{2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if len(w.recent) != 3 {
		t.Fatalf("recent %v", w.recent)
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
}

// switcherBinds are the default binds of the switcher.
var switcherBinds = map[string]string{
	"Cmd+Tab":       string(ActionSwitchColumnNext),
	"Cmd+Shift+Tab": string(ActionSwitchColumnPrev),
	"Cmd+f":         string(ActionToggleFullscreen),
	"F9":            string(ActionSwitchColumnNext),
}

const (
	keyTab   = 15
	keySuper = 133
)

// switcherCore is indicatorCore with the switcher binds, up to three
// columns on screen and n one-window columns (windows 1..n, n focused),
// published once so the configures carry the windows' sizes.
func switcherCore(t *testing.T, n int) (*Core, *indicatorClock) {
	t.Helper()
	commands := make(chan ports.ClientCommand, 64)
	c, ic := indicatorCoreWith(t, commands)
	switcherCommands[c] = commands
	t.Cleanup(func() { delete(switcherCommands, c) })
	cfg := c.cfg
	cfg.Layout.MaxColumns = 3
	cfg.Binds = switcherBinds
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	for id := WindowID(1); id <= WindowID(n); id++ {
		c.cur().mon.AddWindow(id)
	}
	indicatorScene(t, c)
	return c, ic
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

// switcherCommands is the receiving end of each switcherCore's commands.
var switcherCommands = map[*Core]chan ports.ClientCommand{}

// forwarded drains the commands and returns the forwarded keys.
func forwarded(c *Core) []ports.ForwardKey {
	var out []ports.ForwardKey
	ch := switcherCommands[c]
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
	m.sw = switcherState{open: true, ws: w, order: w.switchOrder(), at: at}
}

func TestSwitchCommitFocusesSelectedColumn(t *testing.T) {
	m, w := switcherMonitor(OverflowScroll, 3)
	openSwitcher(m, 1)
	want := m.sw.order[1]
	m.commitSwitcher()
	if id, _ := w.Focused(); id != want || m.sw.open {
		t.Fatalf("focus %d (want %d), switcher open %v", id, want, m.sw.open)
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
	if id, _ := w.Focused(); id != before || m.sw.open {
		t.Fatalf("focus %d, want %d; open %v", id, before, m.sw.open)
	}
	openSwitcher(m, 1)
	m.sw.ws = &Workspace{}
	m.commitSwitcher()
	if id, _ := w.Focused(); id != before || m.sw.open {
		t.Fatalf("focus %d for a foreign workspace, want %d", id, before)
	}
}

// --- P2.2: actions and step ---

func TestSwitchStepOpensOnPreviousColumnAndCycles(t *testing.T) {
	c, ic := switcherCore(t, 3)
	c.mods = ports.ModSuper
	before := switcherFocus(c)
	if err := c.runBind(context.Background(), ActionSwitchColumnNext); err != nil {
		t.Fatal(err)
	}
	sw := &c.cur().mon.sw
	if !sw.open || sw.at != 1 || switcherFocus(c) != before {
		t.Fatalf("open %v at %d focus %d (was %d)", sw.open, sw.at, switcherFocus(c), before)
	}
	if len(ic.timers) != 1 || ic.timers[0] != switcherDelay {
		t.Fatalf("timers %v, want one of %v", ic.timers, switcherDelay)
	}
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if sw.at != 2 {
		t.Fatalf("at %d", sw.at)
	}
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if sw.at != 0 {
		t.Fatalf("at %d, want wrap to 0", sw.at)
	}
	c.runBind(context.Background(), ActionSwitchColumnPrev)
	if sw.at != 2 {
		t.Fatalf("at %d, want wrap to 2", sw.at)
	}
	c.runBind(context.Background(), ActionSwitchColumnPrev)
	if sw.at != 1 {
		t.Fatalf("at %d", sw.at)
	}
	if len(ic.timers) != 1 {
		t.Fatalf("timers %v: stepping must not re-arm", ic.timers)
	}
}

func TestSwitchPrevOpensOnOldestColumn(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.mods = ports.ModSuper
	c.runBind(context.Background(), ActionSwitchColumnPrev)
	if sw := c.cur().mon.sw; !sw.open || sw.at != len(sw.order)-1 {
		t.Fatalf("at %d of %d", sw.at, len(sw.order))
	}
}

func TestSwitchWithoutCmdCommitsAtOnce(t *testing.T) {
	c, ic := switcherCore(t, 3)
	c.mods = 0
	start := switcherFocus(c)
	if err := c.runBind(context.Background(), ActionSwitchColumnNext); err != nil {
		t.Fatal(err)
	}
	if c.cur().mon.sw.open || c.switcher.timerC != nil || len(ic.timers) != 0 {
		t.Fatalf("open %v, timer %v %v", c.cur().mon.sw.open, c.switcher.timerC != nil, ic.timers)
	}
	if switcherFocus(c) == start {
		t.Fatal("focus did not move")
	}
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if switcherFocus(c) != start {
		t.Fatalf("focus %d, want back on %d", switcherFocus(c), start)
	}
}

func TestSwitchIgnoredWithOneColumnOrOverview(t *testing.T) {
	c, _ := switcherCore(t, 1)
	c.mods = ports.ModSuper
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if c.cur().mon.sw.open || c.switcher.sc != nil {
		t.Fatal("switcher opened with one column")
	}
	c, _ = switcherCore(t, 3)
	c.cur().mon.ToggleOverview()
	before := switcherFocus(c)
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if c.cur().mon.sw.open || c.switcher.sc != nil || switcherFocus(c) != before {
		t.Fatal("switcher acted in the overview")
	}
}

// --- P2.3: release commit and timer ---

func TestSwitchQuickTapSwapsWithoutCards(t *testing.T) {
	c, _ := switcherCore(t, 3)
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
		if c.cur().mon.sw.open || c.switcher.sc != nil || c.switcher.timerC != nil {
			t.Fatal("switcher left open")
		}
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
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	order := c.cur().mon.Current().switchOrder()
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true), tab(ports.ModSuper, false))
	if switcherFocus(c) != start || !c.cur().mon.sw.open {
		t.Fatal("focus moved or switcher closed before the release")
	}
	forwarded(c)
	sendKeys(t, c, superKey(0, false))
	if switcherFocus(c) != order[1] || c.cur().mon.sw.open || c.switcher.timerC != nil {
		t.Fatalf("focus %d (want %d), open %v", switcherFocus(c), order[1], c.cur().mon.sw.open)
	}
	fk := forwarded(c)
	if len(fk) != 1 || fk[0].Key.Keysym != "Super_L" || fk[0].Key.Pressed || fk[0].ID != order[1] {
		t.Fatalf("release not forwarded to the new focus: %+v", fk)
	}
}

func TestSwitcherTickShowsCards(t *testing.T) {
	c, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	if c.cur().mon.sw.shown {
		t.Fatal("shown before the delay")
	}
	if !c.switcherTick() || !c.cur().mon.sw.shown {
		t.Fatal("tick did not show the switcher")
	}
	if c.switcher.timerC != nil {
		t.Fatal("timer left")
	}
}

func TestSwitcherTickAfterCloseIsHarmless(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.mods = ports.ModSuper
	c.runBind(context.Background(), ActionSwitchColumnNext)
	c.cancelSwitcher()
	if c.switcherTick() {
		t.Fatal("tick on a closed switcher asks for a scene")
	}
}

// --- P3.1: keys and gestures ---

func TestSwitchEscapeCancelsAndIsNotForwarded(t *testing.T) {
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true), tab(ports.ModSuper, false))
	forwarded(c)
	esc := ports.KeyEvent{Keysym: "Escape", Keycode: 1, Mods: ports.ModSuper, Pressed: true}
	sendKeys(t, c, esc)
	esc.Pressed = false
	sendKeys(t, c, esc, superKey(0, false))
	if c.cur().mon.sw.open || c.switcher.sc != nil || switcherFocus(c) != start {
		t.Fatalf("open %v focus %d (want %d)", c.cur().mon.sw.open, switcherFocus(c), start)
	}
	for _, k := range forwarded(c) {
		if k.Key.Keysym == "Escape" {
			t.Fatalf("Escape forwarded: %+v", k)
		}
	}
}

func TestSwitchAnotherBindCancelsThenRuns(t *testing.T) {
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	sendKeys(t, c, ports.KeyEvent{Keysym: "f", Keycode: 33, Mods: ports.ModSuper, Pressed: true})
	if c.cur().mon.sw.open || c.switcher.sc != nil || c.switcher.timerC != nil {
		t.Fatal("switcher still open")
	}
	if switcherFocus(c) != start {
		t.Fatalf("focus %d moved", switcherFocus(c))
	}
	if c.cur().mon.Current().fullscreen != start {
		t.Fatalf("bind not applied: fullscreen %d", c.cur().mon.Current().fullscreen)
	}
}

func TestSwitchShiftTabGoesBackward(t *testing.T) {
	c, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), shiftTab(true))
	sw := c.cur().mon.sw
	if !sw.open || sw.at != len(sw.order)-1 {
		t.Fatalf("open %v at %d of %d", sw.open, sw.at, len(sw.order))
	}
	sendKeys(t, c, shiftTab(false), tab(ports.ModSuper, true))
	if c.cur().mon.sw.at != 0 {
		t.Fatalf("at %d, want 0 after next", c.cur().mon.sw.at)
	}
}

func TestSwitchSwipeBeginCancels(t *testing.T) {
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	if c.cur().mon.sw.open || c.switcher.sc != nil || c.switcher.timerC != nil || switcherFocus(c) != start {
		t.Fatal("swipe did not cancel the switcher")
	}
}

func TestSwitchSecurityChangeCancels(t *testing.T) {
	c, _ := switcherCore(t, 3)
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
	if c.cur().mon.sw.open || c.switcher.sc != nil || c.switcher.timerC != nil || switcherFocus(c) != start {
		t.Fatal("security change did not cancel the switcher")
	}
}

func TestSwitchCmdKeyFollowsConfig(t *testing.T) {
	c, _ := switcherCore(t, 3)
	cfg := c.cfg
	cfg.Keyboard.CmdKey = "alt"
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	order := c.cur().mon.Current().switchOrder()
	sendKeys(t, c, tab(ports.ModAlt, true))
	if !c.cur().mon.sw.open {
		t.Fatal("Alt+Tab did not open the switcher")
	}
	sendKeys(t, c, ports.KeyEvent{Keysym: "Alt_L", Keycode: 64, Pressed: false})
	if c.cur().mon.sw.open || switcherFocus(c) != order[1] {
		t.Fatalf("open %v focus %d, want %d", c.cur().mon.sw.open, switcherFocus(c), order[1])
	}
}

// --- P3.2: layout ---

func TestSwitcherLayoutCards(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.cfg.Floating.Dim = 0.4
	area := c.cur().mon.Current().overviewArea()
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	if s := indicatorScene(t, c); len(scenePreviews(s)) != 0 {
		t.Fatalf("cards before the delay: %+v", s.Windows)
	}
	order := c.cur().mon.sw.order
	c.switcherTick()
	s := indicatorScene(t, c)
	cards := scenePreviews(s)
	if len(cards) != 3 {
		t.Fatalf("%d cards: %+v", len(cards), s.Windows)
	}
	minX, maxX := 1<<30, 0
	focused := 0
	for k, card := range cards {
		if card.ID != order[k] {
			t.Fatalf("card %d is window %d, want %d (MRU order)", k, card.ID, order[k])
		}
		if card.Rect.W < cards[0].Rect.W-1 || card.Rect.W > cards[0].Rect.W+1 {
			t.Fatalf("cards of different widths: %+v", cards)
		}
		minX, maxX = min(minX, card.Rect.X), max(maxX, card.Rect.X+card.Rect.W)
		if card.Focused {
			focused++
			if card.ID != order[c.cur().mon.sw.at] {
				t.Fatalf("focused card %d is not the selected %d", card.ID, order[c.cur().mon.sw.at])
			}
		}
	}
	left, right := minX-area.X, area.X+area.W-maxX
	if d := left - right; d < -1 || d > 1 {
		t.Fatalf("row not centered: left gap %d, right gap %d", left, right)
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
	// The keyboard stays on the real focus.
	if c.keyboardFocus() != focusedIDReal(c) {
		t.Fatalf("keyboard focus %d moved", c.keyboardFocus())
	}
	// A commit gives the workspace back.
	sendKeys(t, c, superKey(0, false))
	if s := lastScene(t, c); len(scenePreviews(s)) != 0 {
		t.Fatalf("cards after the commit: %+v", s.Windows)
	}
}

func focusedIDReal(c *Core) WindowID {
	id, _ := c.cur().mon.Current().Focused()
	return id
}

func TestSwitcherLayoutHidesOtherWindowsOfColumns(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.cur().mon.AddWindow(4)
	w := c.cur().mon.Current()
	w.ConsumeOrExpel(-1)
	if len(w.Columns) != 3 || w.columnWindows() != 4 {
		t.Fatalf("setup: %d columns, %d windows", len(w.Columns), w.columnWindows())
	}
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	s := indicatorScene(t, c)
	reps := map[WindowID]bool{}
	for _, id := range c.cur().mon.sw.order {
		reps[id] = true
	}
	for _, sw := range s.Windows {
		if reps[sw.ID] {
			if sw.Preview == 0 || sw.Hidden {
				t.Fatalf("representative %d not a card: %+v", sw.ID, sw)
			}
		} else if !sw.Hidden {
			t.Fatalf("window %d not hidden: %+v", sw.ID, sw)
		}
	}
}

func TestSwitcherCardsKeepKeysOnRealFocus(t *testing.T) {
	c, _ := switcherCore(t, 3)
	focus := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	forwarded(c)
	if c.keyboardFocus() != focus {
		t.Fatalf("keyboard focus %d, want %d", c.keyboardFocus(), focus)
	}
	sendKeys(t, c, ports.KeyEvent{Keysym: "x", Keycode: 45, Mods: ports.ModSuper, Pressed: true})
	// An unbound key goes to the real focus (Cmd+x has no bind here).
	fk := forwarded(c)
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
	c, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	target := c.cur().mon.sw.order[2]
	c.cursorX, c.cursorY = cardCenter(t, c, target)
	forwarded(c)
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if c.cur().mon.sw.open || c.switcher.sc != nil || switcherFocus(c) != target {
		t.Fatalf("open %v focus %d, want %d", c.cur().mon.sw.open, switcherFocus(c), target)
	}
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110}); err != nil {
		t.Fatal(err)
	}
	ch := switcherCommands[c]
	for len(ch) > 0 {
		if v, ok := (<-ch).(ports.PointerButtonTo); ok {
			t.Fatalf("click reached a client: %+v", v)
		}
	}
}

func TestSwitcherClickElsewhereCancels(t *testing.T) {
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	indicatorScene(t, c)
	c.cursorX, c.cursorY = 1, 1
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: 0x110, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if c.cur().mon.sw.open || c.switcher.sc != nil || switcherFocus(c) != start {
		t.Fatalf("open %v focus %d, want %d", c.cur().mon.sw.open, switcherFocus(c), start)
	}
}

func TestSwitcherPressBeforeCardsCancelsAndContinues(t *testing.T) {
	c, _ := switcherCore(t, 3)
	start := switcherFocus(c)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	handled, err := c.switcherClick(context.Background(), 0x110)
	if err != nil || handled {
		t.Fatalf("handled %v err %v", handled, err)
	}
	if c.cur().mon.sw.open || c.switcher.timerC != nil || switcherFocus(c) != start {
		t.Fatal("press did not cancel")
	}
}

// --- P3.4: robustness ---

func TestSwitcherClosingSelectedWindowKeepsSelectionValid(t *testing.T) {
	c, _ := switcherCore(t, 3)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	sw := &c.cur().mon.sw
	gone := sw.order[sw.at]
	c.cur().mon.RemoveWindow(gone)
	if !sw.open || len(sw.order) != 2 || sw.at < 0 || sw.at >= len(sw.order) || slices.Contains(sw.order, gone) {
		t.Fatalf("open %v order %v at %d", sw.open, sw.order, sw.at)
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

func TestSwitcherClosingBeforeSelectionKeepsCard(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.mods = ports.ModSuper
	c.runBind(context.Background(), ActionSwitchColumnPrev)
	sw := &c.cur().mon.sw
	selected := sw.order[sw.at]
	c.cur().mon.RemoveWindow(sw.order[0])
	if !sw.open || sw.order[sw.at] != selected {
		t.Fatalf("selection moved from %d: order %v at %d", selected, sw.order, sw.at)
	}
}

func TestSwitcherClosingDownToOneColumnClosesIt(t *testing.T) {
	c, _ := switcherCore(t, 2)
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	c.cur().mon.RemoveWindow(c.cur().mon.sw.order[1])
	if c.cur().mon.sw.open || c.cur().mon.sw.shown {
		t.Fatal("switcher still open with one column")
	}
	if s := indicatorScene(t, c); len(scenePreviews(s)) != 0 {
		t.Fatalf("cards left: %+v", s.Windows)
	}
	// The timer fires on a closed switcher: harmless.
	if c.switcherTick() {
		t.Fatal("tick on a closed switcher")
	}
	sendKeys(t, c, superKey(0, false))
}

func TestSwitcherOutputRemovalCancels(t *testing.T) {
	c, _ := switcherCore(t, 3)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	sendKeys(t, c, superKey(ports.ModSuper, true), tab(ports.ModSuper, true))
	c.switcherTick()
	sc := c.switcher.sc
	if sc == nil {
		t.Fatal("no switcher screen")
	}
	c.removeScreen(sc.name())
	if c.switcher.sc != nil || c.switcher.timerC != nil || sc.mon.sw.open {
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
	c, _ := switcherCore(t, 3)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	first := c.cur()
	for id := WindowID(11); id <= 13; id++ {
		c.screens[c.screenIndex("B")].mon.AddWindow(id)
	}
	indicatorScene(t, c)
	c.mods = ports.ModSuper
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if c.switcher.sc != first {
		t.Fatal("switcher not on the focused screen")
	}
	c.focusScreen = c.screenIndex("B")
	c.runBind(context.Background(), ActionSwitchColumnNext)
	if first.mon.sw.open {
		t.Fatal("old switcher left open")
	}
	if c.switcher.sc != c.cur() || !c.cur().mon.sw.open || c.cur().mon.sw.at != 1 {
		t.Fatalf("new switcher on %v open %v", c.switcher.sc == c.cur(), c.cur().mon.sw.open)
	}
}

// With animations on, the cards fly in and the commit flies them back; the
// windows end where the layout puts them, with no card left.
func TestSwitcherAnimatedHoldAndCommit(t *testing.T) {
	c, ic := switcherCore(t, 3)
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
}
