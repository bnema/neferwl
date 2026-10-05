package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// hiddenCapture is a 300x200 output whose first workspace (windows 1 and 2,
// a 100x100 viewport) is off screen, captured.
func hiddenCapture(t *testing.T) (*Core, *Workspace) {
	t.Helper()
	c, ws, _ := hiddenCaptureCommands(t)
	return c, ws
}

// hiddenCaptureCommands is hiddenCapture with the channel core sends its commands on.
func hiddenCaptureCommands(t *testing.T) (*Core, *Workspace, chan ports.ClientCommand) {
	t.Helper()
	commands := make(chan ports.ClientCommand, 64)
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Border.Width = 2
	cfg.Background.Color = "#102030"
	c, err := New(cfg, Channels{
		Scenes: make(chan []ports.Scene, 1), Commands: commands,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	m := c.screens[0].mon
	ws := m.Current()
	ws.SetSize(100, 100)
	m.AddWindow(1)
	m.AddWindow(2)
	m.Focus(1)
	if m.Current() == ws {
		t.Fatal("workspace still on screen")
	}
	c.configures.cw.load(c.screens[0], ws)
	return c, ws, commands
}

func TestCaptureConfigureSizesHiddenWindowWithoutShowingIt(t *testing.T) {
	c, _ := hiddenCapture(t)
	sc := c.screens[0]
	phys := Placement{ID: 1, Hidden: true}
	t0 := configureTarget{output: "A", area: sc.mon.Frame()}
	cp, tt := c.captureConfigure(sc, phys, t0)
	if cp == nil || cp.Hidden {
		t.Fatalf("no real placement: %+v", cp)
	}
	v, send := c.configures.nextWithCapture(phys, tt, cp)
	want := c.clientRect(*cp)
	if !send || v.Width != want.W || v.Height != want.H || want.W == 0 {
		t.Fatalf("configure %+v send %v want size %+v", v, send, want)
	}
	if v.Visible || v.Activated || !v.Captured || v.Output != "A" {
		t.Fatalf("captured hidden window must be sized, unfocused, physically invisible: %+v", v)
	}
	c.configures.mark(v)
	if _, send := c.configures.nextWithCapture(phys, tt, cp); send {
		t.Fatal("unchanged captured configure sent again")
	}
	// The session stops: the window is only hidden again, keeps its size and drops Captured.
	c.configures.cw.reset()
	cp, tt = c.captureConfigure(sc, phys, t0)
	if cp != nil {
		t.Fatal("placement kept after the session")
	}
	v2, send := c.configures.nextWithCapture(phys, tt, cp)
	if !send || v2.Captured || v2.Visible || v2.Width != v.Width || v2.Height != v.Height {
		t.Fatalf("released window %+v send %v after %+v", v2, send, v)
	}
}

func TestCaptureConfigureLeavesOtherWindowsAlone(t *testing.T) {
	c, _ := hiddenCapture(t)
	sc := c.screens[0]
	phys := Placement{ID: 99, Hidden: true}
	t0 := configureTarget{output: "A", area: sc.mon.Frame()}
	if cp, tt := c.captureConfigure(sc, phys, t0); cp != nil || tt != t0 {
		t.Fatalf("window of another workspace captured: %+v", cp)
	}
	v, _ := c.configures.nextWithCapture(phys, t0, nil)
	if v.Captured || v.Visible || v.Width != 0 {
		t.Fatalf("plain hidden configure %+v", v)
	}
}

func TestCaptureSceneRebasesOnViewport(t *testing.T) {
	c, ws := hiddenCapture(t)
	f := ws.Output
	if f != (Rect{X: 100, Y: 50, W: 100, H: 100}) {
		t.Fatalf("viewport %+v", f)
	}
	c.popups[10] = &popupState{id: 10, parent: 1, rect: Rect{X: 3, Y: 4, W: 10, H: 8}, mapped: true}
	c.popupOrder = append(c.popupOrder, 10)
	s := c.captureScene(7)
	if s == nil || s.OutputWidth != 100 || s.OutputHeight != 100 || s.WorkspaceClip != (Rect{W: 100, H: 100}) || s.Seq != 7 || s.Output != "A" {
		t.Fatalf("scene %+v", s)
	}
	if s.Capture != nil || s.CaptureScene != nil || len(s.Layers) != 0 || s.Background != "#102030" || s.Border.Width != 2 {
		t.Fatalf("capture scene must be flat, layerless and use the normal config: %+v", s)
	}
	byID := map[WindowID]ports.SceneWindow{}
	for _, w := range s.Windows {
		byID[w.ID] = w
	}
	for _, p := range ws.Layout() {
		w := byID[p.ID]
		if want := (Rect{X: p.Rect.X - f.X, Y: p.Rect.Y - f.Y, W: p.Rect.W, H: p.Rect.H}); w.Rect != want || w.Focused {
			t.Fatalf("window %d rect %+v want %+v focused %v", p.ID, w.Rect, want, w.Focused)
		}
		if w.Rect.X < 0 || w.Rect.Y < 0 || w.Rect.X+w.Rect.W > 100 || w.Rect.Y+w.Rect.H > 100 {
			t.Fatalf("window %d outside its viewport: %+v", p.ID, w.Rect)
		}
	}
	client := c.clientRect(ws.Layout()[0])
	pop := byID[10]
	if !pop.Popup || pop.Rect != (Rect{X: client.X - f.X + 3, Y: client.Y - f.Y + 4, W: 10, H: 8}) {
		t.Fatalf("popup %+v client %+v", pop, client)
	}
	for _, sep := range s.Separators {
		if sep.Rect.X < 0 || sep.Rect.Y < 0 || sep.Rect.X+sep.Rect.W > 100 || sep.Rect.Y+sep.Rect.H > 100 {
			t.Fatalf("separator outside the viewport: %+v", sep)
		}
	}
	c.configures.cw.reset()
	if c.captureScene(8) != nil {
		t.Fatal("scene without a captured workspace")
	}
}

func TestCapturedPopupsSurviveButHiddenWindowsTakeNoInput(t *testing.T) {
	c, _ := hiddenCapture(t)
	c.popups[10] = &popupState{id: 10, parent: 1, rect: Rect{X: 3, Y: 4, W: 10, H: 8}, mapped: true}
	c.popupOrder = append(c.popupOrder, 10)
	if _, _, ok := c.windowRect(10); ok {
		t.Fatal("captured popup is physically on screen")
	}
	if _, _, ok := c.windowRect(1); ok || c.visible(1) {
		t.Fatal("captured window is physically on screen")
	}
	if id, _, _ := c.hit(150, 100); id != 0 {
		t.Fatalf("pointer hit hidden window %d", id)
	}
	if sc := c.screens[0]; len(c.scenePopups(sc)) != 0 {
		t.Fatal("captured popup drawn on the physical scene")
	}
	if err := c.closeHiddenPopups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.popups[10] == nil {
		t.Fatal("popup of a captured window dismissed")
	}
	c.configures.cw.reset()
	if err := c.closeHiddenPopups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.popups[10] != nil {
		t.Fatal("popup of a hidden window kept after the session")
	}
}

func TestCaptureWorkspaceReusesStorage(t *testing.T) {
	c, ws := hiddenCapture(t)
	sc := c.screens[0]
	cw := &c.configures.cw
	layoutAllocs := testing.AllocsPerRun(50, func() { _ = ws.Layout() })
	loadAllocs := testing.AllocsPerRun(50, func() { cw.load(sc, ws) })
	if loadAllocs > layoutAllocs {
		t.Fatalf("load allocates %v, more than the layout it wraps (%v)", loadAllocs, layoutAllocs)
	}
	if got := testing.AllocsPerRun(50, func() {
		cw.reset()
		cw.load(sc, ws)
	}); got > layoutAllocs {
		t.Fatalf("reset+load allocates %v, layout %v", got, layoutAllocs)
	}
}

// While the captured workspace itself slides, the live (Active) session keeps
// tracking it in its own viewport: its windows stay captured and its popups
// stay, and both are forgotten when the session ends.
func TestCapturedWorkspaceStaysCapturedWhileSliding(t *testing.T) {
	c, ws, _ := hiddenCaptureCommands(t)
	c.configures.cw.reset()
	c.capt.sessions = []*capSession{{open: ports.CaptureSessionOpen{ID: 1, Workspace: ws.ID}}}
	c.popups[10] = &popupState{id: 10, parent: 1, rect: Rect{X: 3, Y: 4, W: 10, H: 8}, mapped: true}
	c.popupOrder = append(c.popupOrder, 10)
	ws.view.motion = newMotion(viewSpring(10, 0), time.Time{}, 1)
	ctx := context.Background()
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if v := c.capt.sessions[0].last; !v.Hidden || !v.Active || v.Reason.Terminal() {
		t.Fatalf("session state %+v", v)
	}
	if !c.configures.cw.active() || c.popups[10] == nil {
		t.Fatal("sliding session lost its captured workspace or popup")
	}
	if v, ok := c.configures.sent[1]; !ok || !v.Captured || v.Visible || v.Activated || v.Width <= 0 {
		t.Fatalf("window during the slide %+v (sent %v)", v, ok)
	}
	ws.view.motion = motion{}
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if !c.configures.cw.active() {
		t.Fatalf("captured workspace lost after the slide: %+v", c.capt.sessions[0].last)
	}
	// The session ends: the captured workspace is forgotten and its popup goes.
	c.captureClose(1)
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if c.configures.cw.active() || c.popups[10] != nil {
		t.Fatal("captured workspace or popup kept after the session")
	}
	if v, ok := c.configures.sent[1]; !ok || v.Captured || v.Visible || v.Width <= 0 {
		t.Fatalf("window after the session %+v (sent %v)", v, ok)
	}
}

// In the overview the captured workspace's windows keep their real size and
// their physical state; nothing is sized as a thumbnail.
func TestCapturedWorkspaceInOverviewKeepsRealSize(t *testing.T) {
	c, ws, _ := hiddenCaptureCommands(t)
	c.configures.cw.reset()
	m := c.screens[0].mon
	m.Focus(0)
	if m.Current() != ws {
		t.Fatal("workspace not current")
	}
	ctx := context.Background()
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	before := map[WindowID]ports.ConfigureWindow{}
	for _, p := range ws.Layout() {
		v, ok := c.configures.sent[p.ID]
		if !ok || v.Width <= 0 || !v.Visible || v.Captured {
			t.Fatalf("window %d before the overview %+v (sent %v)", p.ID, v, ok)
		}
		before[p.ID] = v
	}
	m.ToggleOverview()
	c.capt.sessions = []*capSession{{open: ports.CaptureSessionOpen{ID: 1, Workspace: ws.ID}}}
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if !c.configures.cw.active() {
		t.Fatal("overview did not track the captured workspace")
	}
	for _, p := range ws.Layout() {
		v, ok := c.configures.sent[p.ID]
		if !ok {
			t.Fatalf("window %d has no configure in the overview", p.ID)
		}
		// A preview keeps the client's real size and its physical state; it
		// is on screen, so it is not Captured.
		if v.Width != before[p.ID].Width || v.Height != before[p.ID].Height || v.Width != c.clientRect(p).W || v.Height != c.clientRect(p).H {
			t.Fatalf("window %d configured %dx%d in the overview, real %dx%d", p.ID, v.Width, v.Height, c.clientRect(p).W, c.clientRect(p).H)
		}
		if !v.Visible || v.Captured || v.Activated != before[p.ID].Activated {
			t.Fatalf("window %d shown as a preview but %+v, was %+v", p.ID, v, before[p.ID])
		}
	}
}
