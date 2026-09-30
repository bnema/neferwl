package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

type captureRig struct {
	client     chan ports.ClientEvent
	output     chan ports.OutputEvent
	commands   chan ports.ClientCommand
	scenes     chan []ports.Scene
	workspaces chan ports.Workspaces
}

// captureCore runs a core with one 100x80 output OUT-1; a nil clock gets a
// keep-alive timer that never fires.
func captureCore(t *testing.T, clock ports.Clock) *captureRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.Border.Width = 0
	r := &captureRig{
		client:     make(chan ports.ClientEvent, 16),
		output:     make(chan ports.OutputEvent, 8),
		commands:   make(chan ports.ClientCommand, 64),
		scenes:     make(chan []ports.Scene, 1),
		workspaces: make(chan ports.Workspaces, 1),
	}
	if clock == nil {
		// The keep-alive timer never fires.
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().Stop().Return(true).Maybe()
		timer.EXPECT().C().Return((<-chan time.Time)(nil)).Maybe()
		mc := portsmocks.NewMockClock(t)
		mc.EXPECT().Now().RunAndReturn(time.Now).Maybe()
		mc.EXPECT().NewTimer(ports.CaptureSessionTimeout).Return(timer).Maybe()
		clock = mc
	}
	c, err := core.New(cfg, core.Channels{Client: r.client, Output: r.output, Commands: r.commands, Scenes: r.scenes, Workspaces: r.workspaces, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	r.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, r.scenes)
	return r
}

// state returns the next CaptureSessionState, skipping other commands.
func (r *captureRig) state(t *testing.T) ports.CaptureSessionState {
	t.Helper()
	for {
		if v, ok := receive(t, r.commands).(ports.CaptureSessionState); ok {
			return v
		}
	}
}

// sceneWith returns the next scene whose capture presence equals want.
func (r *captureRig) sceneWith(t *testing.T, want bool) ports.Scene {
	t.Helper()
	for {
		if s := scene(t, r.scenes); (s.Capture != nil) == want {
			return s
		}
	}
}

func (r *captureRig) layer(id ports.WindowID, layer ports.Layer, keyboard uint32) ports.LayerSurface {
	return ports.LayerSurface{ID: id, Layer: layer, Anchor: ports.AnchorTop | ports.AnchorLeft, Width: 10, Height: 10, Output: "OUT-1", Keyboard: keyboard}
}

func TestCaptureSessionOutputTarget(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1", Region: ports.Rect{X: -10, Y: 10, W: 50, H: 200}, Record: true}
	st := r.state(t)
	if !st.Active || st.Workspace != 0 || st.Rect != (ports.Rect{X: 0, Y: 10, W: 40, H: 70}) {
		t.Fatalf("state %+v", st)
	}
	s := r.sceneWith(t, true)
	want := &ports.SceneCapture{Session: 1, TargetRect: st.Rect, Excluded: []ports.WindowID{}, BorderWidth: ports.CaptureBorderWidth, BorderColor: ports.CaptureBorderColor}
	if s.Capture.Session != 1 || s.Capture.TargetRect != want.TargetRect || len(s.Capture.Excluded) != 0 || s.Capture.BorderWidth != 2 || s.Capture.BorderColor != "#ff3b30" || s.Capture.Workspace != 0 {
		t.Fatalf("capture %+v", s.Capture)
	}
	// Without Record there is no border, and a zero region is the whole output.
	r.client <- ports.CaptureSessionEnd{ID: 1}
	r.sceneWith(t, false)
	r.client <- ports.CaptureSessionBegin{ID: 2, Output: "OUT-1"}
	if st := r.state(t); st.Rect != (ports.Rect{W: 100, H: 80}) {
		t.Fatalf("state %+v", st)
	}
	if s := r.sceneWith(t, true); s.Capture.BorderWidth != 0 || s.Capture.BorderColor != "" {
		t.Fatalf("border without record: %+v", s.Capture)
	}
}

func TestCaptureSessionWorkspaceTarget(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	ws := receive(t, r.workspaces).Outputs[0].Workspaces
	if ws[0].Frame != (ports.Rect{W: 100, H: 80}) {
		t.Fatalf("frame %+v", ws[0])
	}
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1", Workspace: ws[0].ID}
	if st := r.state(t); !st.Active || st.Workspace != ws[0].ID || st.Rect != ws[0].Frame {
		t.Fatalf("state %+v", st)
	}
	if s := r.sceneWith(t, true); s.Capture.Workspace != ws[0].ID {
		t.Fatalf("capture %+v", s.Capture)
	}
	r.client <- ports.CaptureSessionEnd{ID: 1}
	r.sceneWith(t, false)
	// A workspace that does not exist ends the session.
	r.client <- ports.CaptureSessionBegin{ID: 2, Output: "OUT-1", Workspace: 9999}
	if st := r.state(t); st.Active || st.Reason != ports.CaptureReasonWorkspaceGone || !st.Reason.Terminal() {
		t.Fatalf("state %+v", st)
	}
}

func TestCaptureSessionTerminalReasons(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "NOPE"}
	if st := r.state(t); st.Reason != ports.CaptureReasonOutputGone || st.Active {
		t.Fatalf("state %+v", st)
	}
	r.client <- ports.CaptureSessionBegin{ID: 2, Output: "OUT-1", Region: ports.Rect{X: 500, Y: 500, W: 5, H: 5}}
	if st := r.state(t); st.Reason != ports.CaptureReasonInvalidRegion {
		t.Fatalf("state %+v", st)
	}
	// An unplugged output ends the session.
	r.client <- ports.CaptureSessionBegin{ID: 3, Output: "OUT-1"}
	if st := r.state(t); !st.Active {
		t.Fatalf("state %+v", st)
	}
	r.output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-2", Width: 100, Height: 80}}
	r.output <- ports.OutputRemoved{Name: "OUT-1"}
	if st := r.state(t); st.ID != 3 || st.Reason != ports.CaptureReasonOutputGone {
		t.Fatalf("state %+v", st)
	}
	r.sceneWith(t, false)
}

func TestCaptureSessionBusy(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	if st := r.state(t); st.ID != 1 || !st.Active {
		t.Fatalf("state %+v", st)
	}
	r.client <- ports.CaptureSessionBegin{ID: 2, Output: "OUT-1"}
	if st := r.state(t); st.ID != 2 || st.Reason != ports.CaptureReasonBusy || st.Active {
		t.Fatalf("state %+v", st)
	}
	// The first session is undisturbed and ends only on its own request.
	r.client <- ports.CaptureSessionEnd{ID: 2}
	r.client <- ports.CaptureSessionEnd{ID: 1}
	r.sceneWith(t, false)
	r.client <- ports.CaptureSessionBegin{ID: 3, Output: "OUT-1"}
	if st := r.state(t); st.ID != 3 || !st.Active {
		t.Fatalf("state %+v", st)
	}
}

// Attached layers stay over a fullscreen window while the session is active,
// are excluded with their popups, and other layers keep hiding.
func TestCaptureSessionLayersOverFullscreen(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	scene(t, r.scenes)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0), r.layer(11, ports.LayerTop, 0)}}
	if s := scene(t, r.scenes); len(s.Layers) != 0 {
		t.Fatalf("fullscreen should hide layers: %+v", s.Layers)
	}
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true} // duplicate
	r.client <- ports.PopupRequest{ID: 20, Parent: 10, Positioner: ports.Positioner{Width: 5, Height: 5, AnchorRect: ports.Rect{W: 1, H: 1}}}
	r.client <- ports.PopupMapped{ID: 20}
	var s ports.Scene
	for {
		s = r.sceneWith(t, true)
		if len(s.Capture.Excluded) == 2 {
			break
		}
	}
	if s.Capture.Excluded[0] != 10 || s.Capture.Excluded[1] != 20 {
		t.Fatalf("excluded %v", s.Capture.Excluded)
	}
	if len(s.Layers) != 1 || s.Layers[0].ID != 10 {
		t.Fatalf("only the attached layer shows over fullscreen: %+v", s.Layers)
	}
	r.client <- ports.CaptureSessionEnd{ID: 1}
	if s = r.sceneWith(t, false); len(s.Layers) != 0 {
		t.Fatalf("layers after the session: %+v", s.Layers)
	}
}

func TestCaptureSessionLayerBounds(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	for id := ports.WindowID(1); id <= ports.MaxCaptureSessionLayers+3; id++ {
		r.client <- ports.CaptureSessionLayer{ID: 1, Layer: id, Attached: true}
	}
	r.client <- ports.CaptureSessionLayer{ID: 99, Layer: 50, Attached: true} // another session
	var s ports.Scene
	for {
		s = r.sceneWith(t, true)
		if len(s.Capture.Excluded) == ports.MaxCaptureSessionLayers {
			break
		}
	}
	for _, id := range s.Capture.Excluded {
		if id > ports.MaxCaptureSessionLayers {
			t.Fatalf("excluded %v", s.Capture.Excluded)
		}
	}
	// Detaching removes one.
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 1}
	for {
		if s = r.sceneWith(t, true); len(s.Capture.Excluded) == ports.MaxCaptureSessionLayers-1 {
			break
		}
	}
}

// An attached layer asking for exclusive keyboard never gets it; on-demand stays.
func TestCaptureSessionKeyboardClamp(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0), r.layer(11, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	r.sceneWith(t, true)
	// The HUD commits exclusive afterwards; an unrelated layer may have it.
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 1), r.layer(11, ports.LayerOverlay, 1)}}
	for len(r.commands) > 0 {
		<-r.commands
	}
	scene(t, r.scenes)
	for {
		f, ok := receive(t, r.commands).(ports.FocusWindow)
		if ok {
			if f.ID != 11 {
				t.Fatalf("keyboard went to %d, want the unattached layer 11", f.ID)
			}
			return
		}
	}
}

func TestCaptureSessionPingTimeout(t *testing.T) {
	fire := make(chan time.Time)
	timer := portsmocks.NewMockTimer(t)
	timer.EXPECT().Stop().Return(true).Maybe()
	timer.EXPECT().C().Return(fire)
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(time.Now).Maybe()
	clock.EXPECT().NewTimer(ports.CaptureSessionTimeout).Return(timer).Once()
	r := captureCore(t, clock)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	if st := r.state(t); !st.Active {
		t.Fatalf("state %+v", st)
	}
	fire <- time.Now()
	if st := r.state(t); st.ID != 1 || st.Reason != ports.CaptureReasonPingTimeout || st.Active {
		t.Fatalf("state %+v", st)
	}
	r.sceneWith(t, false)
}

// A ping restarts the timer; the session then times out from the last one.
func TestCaptureSessionPingRestartsTimer(t *testing.T) {
	first, second := make(chan time.Time), make(chan time.Time)
	t1, t2 := portsmocks.NewMockTimer(t), portsmocks.NewMockTimer(t)
	t1.EXPECT().Stop().Return(true).Once()
	t1.EXPECT().C().Return(first)
	t2.EXPECT().Stop().Return(true).Maybe()
	t2.EXPECT().C().Return(second)
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(time.Now).Maybe()
	clock.EXPECT().NewTimer(ports.CaptureSessionTimeout).Return(t1).Once()
	clock.EXPECT().NewTimer(ports.CaptureSessionTimeout).Return(t2).Once()
	r := captureCore(t, clock)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.state(t)
	r.client <- ports.CaptureSessionPing{ID: 1}
	r.client <- ports.CaptureSessionPing{ID: 7} // unknown: no effect
	r.client <- ports.WindowMapped{ID: 1}       // a round trip through the owner loop
	scene(t, r.scenes)
	second <- time.Now()
	if st := r.state(t); st.Reason != ports.CaptureReasonPingTimeout {
		t.Fatalf("state %+v", st)
	}
}

// Every change a clean capture depends on bumps the revision, in the state
// and in the scene that carries it; an unchanged republish does not.
func TestCaptureSessionRevisionFence(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	st := r.state(t)
	if st.Revision != 1 || len(st.Layers) != 0 {
		t.Fatalf("state %+v", st)
	}
	if s := r.sceneWith(t, true); s.Capture.Revision != 1 {
		t.Fatalf("scene revision %d", s.Capture.Revision)
	}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	st = r.state(t)
	if st.Revision != 2 || len(st.Layers) != 1 || st.Layers[0] != 10 {
		t.Fatalf("state after attach %+v", st)
	}
	var s ports.Scene
	for {
		if s = r.sceneWith(t, true); s.Capture.Revision == 2 {
			break
		}
	}
	if len(s.Capture.Excluded) != 1 || s.Capture.Excluded[0] != 10 {
		t.Fatalf("scene %+v", s.Capture)
	}
	// An unrelated publish keeps the revision and sends no state.
	r.client <- ports.WindowMapped{ID: 1}
	if s = r.sceneWith(t, true); s.Capture.Revision != 2 {
		t.Fatalf("revision moved without a change: %d", s.Capture.Revision)
	}
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.CaptureSessionState); ok {
			t.Fatalf("state without a change: %+v", v)
		}
	}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10}
	if st = r.state(t); st.Revision != 3 || len(st.Layers) != 0 {
		t.Fatalf("state after detach %+v", st)
	}
}

// A detached layer stays excluded until a LayerChanged stops listing it, so
// the last frame of a destroyed HUD never leaks into a capture.
func TestCaptureSessionDetachRetainsExclusion(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	for {
		if s := r.sceneWith(t, true); len(s.Capture.Excluded) == 1 {
			break
		}
	}
	// Detach while the layer is still listed: still excluded.
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10}
	s := r.sceneWith(t, true)
	if len(s.Capture.Excluded) != 1 || s.Capture.Excluded[0] != 10 {
		t.Fatalf("detached layer no longer excluded while mapped: %+v", s.Capture)
	}
	// Once the layer unmaps, the exclusion goes.
	r.client <- ports.LayerChanged{Layers: nil}
	for {
		if s = r.sceneWith(t, true); len(s.Capture.Excluded) == 0 {
			break
		}
	}
}

// An unmap queued before the detach (the wayland order) never leaves a
// window where the layer is listed but not excluded.
func TestCaptureSessionUnmapThenDetach(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	for {
		if s := r.sceneWith(t, true); len(s.Capture.Excluded) == 1 {
			break
		}
	}
	r.client <- ports.LayerChanged{Layers: nil}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10}
	for {
		s := r.sceneWith(t, true)
		if len(s.Layers) != 0 && len(s.Capture.Excluded) == 0 {
			t.Fatalf("layer shown but not excluded: %+v %+v", s.Layers, s.Capture)
		}
		if len(s.Layers) == 0 {
			return
		}
	}
}

// An attached layer never gets keyboard interactivity, exclusive or on demand.
func TestCaptureSessionKeyboardNone(t *testing.T) {
	r := captureCore(t, nil)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 2), r.layer(11, ports.LayerOverlay, 2)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionBegin{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionLayer{ID: 1, Layer: 10, Attached: true}
	r.sceneWith(t, true)
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	for len(r.commands) > 0 {
		if f, ok := (<-r.commands).(ports.FocusWindow); ok && f.ID == 10 {
			t.Fatalf("keyboard went to the attached layer")
		}
	}
}
