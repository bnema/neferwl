package core

import (
	"github.com/bnema/neferwl/internal/ports"
	"math"
	"testing"
)

func sizedMonitor(overflow Overflow) *Monitor {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.SetMaxColumns(2)
	m.SetOverflow(overflow)
	m.SetNamed([]NamedWorkspace{{Name: "s", Size: [2]int{100, 100}}})
	m.ToggleNamed("s")
	return m
}

// Sized transitions remain settled inside their frame rather than exposing
// scrolling columns in the margins. Overview retains the whole monitor.
func TestSizedTransitionsStaySettledAndOverviewSpansMonitor(t *testing.T) {
	m := sizedMonitor(OverflowScroll)
	if got, want := m.Frame(), (Rect{X: 100, Y: 50, W: 100, H: 100}); got != want {
		t.Fatalf("frame %+v want %+v", got, want)
	}
	w := m.Current()
	m.hidden = nil
	m.Workspaces = append(m.Workspaces, w)
	m.Active = len(m.Workspaces) - 1
	m.shown = nil
	m.switchOff = -0.4
	if got := m.Frame(); got != m.Current().Output || !m.framedSwitch() {
		t.Fatalf("sliding frame %+v", got)
	}
	m.AddWindow(1)
	before := m.Current().Layout()
	during := m.slideLayout(before)
	if len(during) != len(before) || during[0].Rect != before[0].Rect {
		t.Fatalf("sized workspace animated outside frame: %+v", during)
	}
	m.switchOff = 0
	if got := m.Frame(); got != m.Current().Output {
		t.Fatalf("settled frame %+v", got)
	}
	m.ToggleOverview()
	if got := m.Frame(); got != m.Output() {
		t.Fatalf("overview frame %+v", got)
	}
}

// A sized neighbor must not disable animation between inherited workspaces.
func TestSizedNeighborDisablesSlideButInheritedNeighborDoesNot(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(300, 200)
	m.AddWindow(1)
	m.Workspaces[1].SetSize(100, 100)
	before := m.Layout()
	m.switchOff = 0.4
	if !m.framedSwitch() || m.Layout()[0].Rect != before[0].Rect {
		t.Fatal("slide toward sized neighbor escaped viewport")
	}
	m.Workspaces[1].SetSize(0, 0)
	if m.framedSwitch() || m.Layout()[0].Rect == before[0].Rect {
		t.Fatal("normal inherited workspace stopped animating")
	}
}

// The overview keeps monitor-wide placement and real client aspect ratios.
func TestOverviewIgnoresWorkspaceSize(t *testing.T) {
	m := sizedMonitor(OverflowScroll)
	for _, mm := range []*Monitor{m} {
		mm.AddWindow(1)
		mm.AddWindow(2)
	}
	real := previewOf(t, m.Layout(), 1).Rect
	m.ToggleOverview()
	p := previewOf(t, m.Layout(), 1)
	if p.Preview <= 0 || p.Rect.W <= 0 {
		t.Fatalf("%+v", p)
	}
	if math.Abs(float64(p.Rect.W)/float64(p.Rect.H)-float64(real.W)/float64(real.H)) > 0.05 {
		t.Fatalf("aspect: preview %+v real %+v", p.Rect, real)
	}
	if math.Abs(float64(p.Rect.W)-float64(real.W)*p.Preview) > 1 {
		t.Fatalf("width %d, zoom %v of %d", p.Rect.W, p.Preview, real.W)
	}
	// Row centered in the 300 wide monitor, not in the 100 wide viewport.
	l, r := previewOf(t, m.Layout(), 1).Rect, previewOf(t, m.Layout(), 2).Rect
	if mid := (l.X + r.X + r.W) / 2; mid < 145 || mid > 155 {
		t.Fatalf("row center %d: %+v %+v", mid, l, r)
	}
	// Vertically centered in the monitor as well, whatever the viewport.
	if mid := l.Y + l.H/2; mid < 95 || mid > 105 {
		t.Fatalf("row middle %d: %+v", mid, l)
	}
}

// The pointer constraint and warp bounds are the part of a client inside the
// viewport, offset by the screen; a client wholly outside has none.
func TestShownClientRectIsClippedToFrame(t *testing.T) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c := newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	s := c.screens[0]
	s.x = 1000
	s.mon.SetNamed([]NamedWorkspace{{Name: "s", Size: [2]int{100, 100}}})
	s.mon.ToggleNamed("s")
	for id := WindowID(1); id <= 3; id++ {
		s.mon.AddWindow(id)
	}
	// Frame is {100,50,100,100}; columns are 50 wide, the focused last one
	// ends at the frame's right edge and the first is scrolled off the left.
	if r, ok := c.shownClientRect(3); !ok || r != (Rect{X: 1150, Y: 50, W: 50, H: 100}) {
		t.Fatalf("visible %+v %v", r, ok)
	}
	if r, ok := c.shownClientRect(1); ok {
		t.Fatalf("scrolled off window has bounds %+v", r)
	}
}

// Toggling a named workspace that is already on the focused screen, or on
// its connected home, allocates nothing to find it.
func TestBringNamedLookupAllocations(t *testing.T) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "s"}, {Name: "h", Monitor: "A"}}
	c := newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 600, Height: 400})
	if n := testing.AllocsPerRun(100, func() { c.focusScreen = 0; c.bringNamed("s"); c.bringNamed("h") }); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}

// newSizedCore returns a core with buffered output channels and no outputs.
func newSizedCore(t *testing.T, cfg ports.Config) *Core {
	t.Helper()
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Layouts: make(chan ports.Layout, 1), Constraints: make(chan ports.PointerConstraint, 1), State: make(chan ports.State, 1), Workspaces: make(chan ports.Workspaces, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
