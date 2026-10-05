package core

import (
	"context"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func namedOverviewMonitor() *Monitor {
	m := overviewMonitor()
	m.SetNamed([]NamedWorkspace{{Name: "dev"}, {Name: "empty"}, {Name: "game"}})
	m.Focus(1)
	m.ToggleNamed("dev")
	m.AddWindow(5)
	m.AddWindow(6)
	m.Current().ToggleWindowStash()
	m.ToggleNamed("game")
	m.AddWindow(7)
	m.Focus(1)
	return m
}

func TestOverviewNamedFollowsInvocationWorkspace(t *testing.T) {
	m := namedOverviewMonitor()
	numbered := slices.Clone(m.Workspaces)
	m.Focus(0)
	m.ToggleNamed("dev")
	m.ToggleNamed("dev")
	m.ToggleOverview()
	if !slices.Equal(m.overviewWorkspaces(), []*Workspace{numbered[0], m.byName("dev"), numbered[1], m.byName("game")}) {
		t.Fatal("named workspace not directly below its invocation workspace")
	}
	m.OverviewMove(0, 1)
	if m.Current().Name != "dev" {
		t.Fatal("down from workspace 1 did not reach invoked named workspace")
	}
	m.OverviewMove(0, 1)
	if m.Current() != numbered[1] {
		t.Fatal("down from named did not resume numbered sequence")
	}
	m.CancelOverview()
	m.Focus(1)
	m.ToggleNamed("dev")
	m.ToggleNamed("dev")
	m.ToggleOverview()
	if !slices.Equal(m.overviewWorkspaces(), []*Workspace{numbered[0], numbered[1], m.byName("dev"), m.byName("game")}) {
		t.Fatal("calling named workspace elsewhere did not update its placement")
	}
	if !slices.Equal(m.Workspaces, numbered) {
		t.Fatal("overview attachment changed workspace numbering")
	}
}

func TestOverviewNamedNavigationDoesNotReattach(t *testing.T) {
	m := namedOverviewMonitor()
	m.Focus(0)
	m.ToggleNamed("dev")
	m.ToggleNamed("dev")
	m.ToggleOverview()
	before := slices.Clone(m.overviewWorkspaces())
	m.OverviewMove(0, 1)
	m.OverviewMove(0, 1)
	m.OverviewMove(0, -1)
	if !slices.Equal(m.overviewWorkspaces(), before) || m.Current().Name != "dev" {
		t.Fatal("browsing from below changed named attachment")
	}
	_, dividers := m.overviewRows()
	if len(dividers) != 2 {
		t.Fatalf("named group needs upper and lower dividers: %+v", dividers)
	}
	m.ToggleOverview()
	m.ToggleNamed("dev")
	if m.Current() != before[0] {
		t.Fatal("entering named from below lost invocation return")
	}
}

func TestOverviewNamedAnchorRemoved(t *testing.T) {
	m := namedOverviewMonitor()
	m.Focus(0)
	m.take(m.Workspaces[1])
	m.ToggleOverview()
	rows := m.overviewWorkspaces()
	if !slices.Equal(rows, []*Workspace{m.Workspaces[0], m.byName("dev"), m.byName("game")}) {
		t.Fatal("removed anchor left named rows unreachable")
	}
	m.OverviewMove(0, 1)
	if m.Current().Name != "dev" {
		t.Fatal("orphan named row not reachable")
	}
}

func TestOverviewNamedAnchorSurvivesConfigAndMonitorMove(t *testing.T) {
	m := namedOverviewMonitor()
	anchor, dev, game := m.Workspaces[1], m.byName("dev"), m.byName("game")
	m.SetNamed([]NamedWorkspace{{Name: "game"}, {Name: "dev"}})
	if !slices.Equal(m.overviewWorkspaces(), []*Workspace{m.Workspaces[0], anchor, game, dev}) {
		t.Fatal("config reorder lost attachment or ignored named order")
	}
	m.Focus(0)
	m.take(anchor)
	m.take(dev)
	m.take(game)
	dst := newMonitor("", "")
	dst.SetOutput(300, 200)
	dst.useSpecs([]NamedWorkspace{{Name: "dev"}, {Name: "game"}})
	dst.adopt(anchor, false, 0)
	dst.adopt(game, true, 0)
	dst.adopt(dev, true, 0)
	rows := dst.overviewWorkspaces()
	if at := slices.Index(rows, anchor); at < 0 || at+2 >= len(rows) || rows[at+1] != dev || rows[at+2] != game {
		t.Fatal("moved named workspaces not reachable on destination monitor")
	}
	if dev.overviewAfter != nil && !dst.has(dev.overviewAfter) {
		t.Fatal("named workspace retained detached monitor anchor")
	}
}

func TestOverviewNamedAnchorSurvivesReplug(t *testing.T) {
	cfg := ports.Config{}
	cfg.Layout.MaxColumns = 3
	cfg.Keyboard.CmdKey = "super"
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "dev", Monitor: "OUT-1"}}
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200})
	home := c.cur()
	home.mon.SetNamed([]NamedWorkspace{{Name: "dev"}})
	home.mon.AddWindow(1)
	anchor := home.mon.Current()
	home.mon.ToggleNamed("dev")
	home.mon.AddWindow(2)
	dev := home.mon.Current()
	home.mon.Focus(1)
	home.mon.AddWindow(3) // a later row makes bottom fallback detectably wrong
	c.addScreen(ports.OutputInfo{Name: "OUT-2", Width: 300, Height: 200})
	c.removeScreen("OUT-1")
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200})
	c.settleGuests()
	for _, sc := range c.screens {
		if sc.name() != "OUT-1" {
			continue
		}
		if !sc.mon.has(dev) || dev.overviewAfter != anchor {
			t.Fatal("replug lost named invocation anchor")
		}
		rows := sc.mon.overviewWorkspaces()
		at := slices.Index(rows, anchor)
		if at < 0 || at+1 >= len(rows) || rows[at+1] != dev {
			t.Fatal("replug did not restore named row below invocation workspace")
		}
		return
	}
	t.Fatal("missing replugged screen")
}

func TestOverviewNamedEmptyAnchor(t *testing.T) {
	m := namedOverviewMonitor()
	m.Focus(len(m.Workspaces) - 1)
	anchor := m.Current()
	m.ToggleNamed("dev")
	m.ToggleOverview()
	rows := m.overviewWorkspaces()
	if at := slices.Index(rows, anchor); at < 0 || at+1 >= len(rows) || rows[at+1] != m.byName("dev") {
		t.Fatal("empty invocation workspace missing above named row")
	}
}

func TestOverviewNamedNavigation(t *testing.T) {
	m := namedOverviewMonitor()
	numbered := slices.Clone(m.Workspaces)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	if m.Current().Name != "dev" {
		t.Fatalf("down from last occupied numbered workspace: %q", m.Current().Name)
	}
	if id := m.card(); id != 6 {
		t.Fatalf("named stash selection: %d", id)
	}
	m.OverviewMove(0, 1)
	if m.Current().Name != "game" || m.card() != 0 {
		t.Fatalf("next named workspace: %q, card %d", m.Current().Name, m.card())
	}
	m.OverviewMove(0, 1)
	if m.Current().Name != "game" {
		t.Fatal("past last named workspace")
	}
	m.OverviewMove(0, -1)
	m.OverviewMove(0, -1)
	if m.Current() != numbered[1] || !slices.Equal(m.Workspaces, numbered) {
		t.Fatal("return to numbered workspaces changed numbering")
	}
	m.CancelOverview()
	m.Focus(0)
	m.ToggleOverview()
	_, dividers := m.overviewRows()
	if len(dividers) != 0 {
		t.Fatalf("divider within numbered group: %+v", dividers)
	}
}

func TestOverviewNamedRowsKeepWidthAndStash(t *testing.T) {
	m := namedOverviewMonitor()
	m.ToggleOverview()
	_, dividers := m.overviewRows()
	if len(dividers) != 1 || dividers[0].Active || dividers[0].Rect.W != 276 {
		t.Fatalf("group divider: %+v", dividers)
	}
	below := previewOf(t, m.Layout(), 5)
	pile := previewOf(t, m.Layout(), 6)
	if below.Hidden || !below.Peek || pile.Hidden || !pile.Peek {
		t.Fatalf("named neighbor and stash not visible: %+v, %+v", below, pile)
	}
	if pile.Preview != overviewCardZoom {
		t.Fatalf("stash zoom %v", pile.Preview)
	}
	m.OverviewMove(0, 1)
	selected := previewOf(t, m.Layout(), 5)
	if selected.Hidden || selected.Rect.X != below.Rect.X || selected.Rect.W != below.Rect.W || selected.Preview != below.Preview {
		t.Fatalf("named row geometry changed: %+v, %+v", below, selected)
	}
	above := previewOf(t, m.Layout(), 4)
	if above.Hidden || !above.Peek || above.Rect.Y >= selected.Rect.Y {
		t.Fatalf("numbered neighbor above named: %+v", above)
	}
	_, dividers = m.overviewRows()
	if len(dividers) != 1 || dividers[0].Rect.Y <= above.Rect.Y+above.Rect.H || dividers[0].Rect.Y >= selected.Rect.Y {
		t.Fatalf("divider not between groups: %+v", dividers)
	}
	game := previewOf(t, m.Layout(), 7)
	if game.Hidden || !game.Peek || game.Rect.Y <= selected.Rect.Y {
		t.Fatalf("next named neighbor: %+v", game)
	}
}

func TestOverviewNamedCancelAndToggleReturn(t *testing.T) {
	m := namedOverviewMonitor()
	m.ToggleNamed("dev")
	back := m.back
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.CancelOverview()
	if m.Current().Name != "dev" || m.back != back {
		t.Fatal("Escape did not restore named workspace and its return target")
	}
	m.Focus(1)
	m.ToggleOverview()
	m.OverviewMove(0, 1)
	m.ToggleOverview()
	m.ToggleNamed("dev")
	if m.Current() != back {
		t.Fatal("named toggle did not return to overview entry workspace")
	}
}

func TestOverviewNamedBrowseKeepsNumberedReturn(t *testing.T) {
	for _, click := range []bool{false, true} {
		m := namedOverviewMonitor()
		back := m.Current()
		m.ToggleOverview()
		m.OverviewMove(0, 1)
		if click {
			m.OverviewPick(7)
		} else {
			m.OverviewMove(0, 1)
			m.ToggleOverview()
		}
		if m.Current().Name != "game" || m.back != back {
			t.Fatalf("click %v: browsing lost numbered return", click)
		}
		m.ToggleNamed("game")
		if m.Current() != back {
			t.Fatalf("click %v: toggle did not return to numbered workspace", click)
		}
	}
}

func TestOverviewWorkspaceBindKeepsCurrentSelection(t *testing.T) {
	m := maximizedOverview()
	m.ToggleOverview()
	m.OverviewMove(0, -1)
	m.OverviewMove(1, 0)
	front, selected := m.stackFront(m.Current()), m.overviewTarget()
	if front.kind != stackColumns || selected != 3 {
		t.Fatal("expected provisional hidden column 3")
	}
	for _, action := range []Action{"focus-workspace 1", "workspace unknown"} {
		m.Apply(action)
		if m.stackFront(m.Current()) != front || m.overviewTarget() != selected {
			t.Fatalf("%s reset provisional selection", action)
		}
	}
}

func TestOverviewNamedNeighborPick(t *testing.T) {
	m := namedOverviewMonitor()
	back := m.Current()
	m.ToggleOverview()
	p := previewOf(t, m.Layout(), 6)
	if p.Hidden {
		t.Fatal("named stash neighbor hidden")
	}
	id := overviewIn(m.Layout(), float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2))
	if id != 6 {
		t.Fatalf("stash hit: %d", id)
	}
	m.OverviewPick(id)
	if m.ov.open || m.Current().Name != "dev" || m.back != back || !m.Current().stashFocused() {
		t.Fatal("named stash click did not commit selection and return target")
	}
}

func TestOverviewNamedFocusBinds(t *testing.T) {
	for _, action := range []Action{ActionFocusWindowDown, ActionFocusWorkspaceDown} {
		t.Run(string(action), func(t *testing.T) {
			m := namedOverviewMonitor()
			m.ToggleOverview()
			m.Apply(action)
			if m.Current().Name != "dev" {
				t.Fatalf("focus bind selected %q", m.Current().Name)
			}
		})
	}
	m := namedOverviewMonitor()
	m.ToggleOverview()
	m.Apply(Action("workspace dev"))
	if m.card() != 6 {
		t.Fatal("named bind did not initialize stash selection")
	}
	m.Apply(Action("focus-workspace 2"))
	if m.card() != 0 || m.ov.row != nil {
		t.Fatal("numbered bind retained previous row selection")
	}
}

func TestOverviewNamedOutsideNavigationUnchanged(t *testing.T) {
	m := namedOverviewMonitor()
	m.Apply(ActionFocusWorkspaceDown)
	if m.shown != nil || !m.Current().empty() {
		t.Fatal("outside overview, down no longer reaches numbered spare")
	}
	m.ToggleNamed("dev")
	m.Apply(ActionFocusWorkspaceUp)
	if m.Current().Name != "dev" {
		t.Fatal("outside overview, named workspace entered numbered list")
	}
}

func TestOverviewNamedEmptyCurrentAndRemoval(t *testing.T) {
	m := namedOverviewMonitor()
	m.ToggleNamed("empty")
	m.ToggleOverview()
	if !m.Current().empty() {
		t.Fatal("expected empty named current")
	}
	m.OverviewMove(0, 1)
	if m.Current().Name != "game" {
		t.Fatal("could not leave empty named row")
	}
	m.SetNamed([]NamedWorkspace{{Name: "game"}})
	m.CancelOverview()
	if !m.has(m.Current()) || m.back != nil && !m.has(m.back) {
		t.Fatal("config removal left dangling overview return")
	}
}

func TestOverviewNamedColumnPickClearsStash(t *testing.T) {
	m := namedOverviewMonitor()
	m.ToggleNamed("dev")
	m.ToggleOverview()
	if m.card() != 6 {
		t.Fatal("expected stash selection")
	}
	m.OverviewPick(5)
	if m.Current().stashFocused() || m.ov.open {
		t.Fatal("column click kept stash selection")
	}
	id, _ := m.Focused()
	if id != 5 {
		t.Fatalf("column click focus %d", id)
	}
}

func TestOverviewNamedDividerPublished(t *testing.T) {
	scenes := make(chan []ports.Scene, 1)
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 3
	c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 256), Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 300, Height: 200})
	c.cur().mon = namedOverviewMonitor()
	c.cur().mon.ToggleOverview()
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	scene := (<-scenes)[0]
	_, dividers := c.cur().mon.overviewRows()
	if len(dividers) != 1 || !slices.Contains(scene.Separators, dividers[0]) {
		t.Fatalf("group divider not published: %+v", scene.Separators)
	}
}

func TestOverviewNamedScrollAndSwipe(t *testing.T) {
	m := namedOverviewMonitor()
	m.ToggleOverview()
	m.overviewFocus(ActionFocusWorkspaceDown)
	if m.Current().Name != "dev" {
		t.Fatal("swipe did not cross groups")
	}
	m.overviewScroll(ports.PointerAxis{Source: ports.AxisWheel, Vertical: ports.ScrollAxis{Set: true, V120: 120}})
	if m.Current().Name != "game" {
		t.Fatal("scroll did not reach next named workspace")
	}
}
