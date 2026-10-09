package core_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

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

// captureCore runs a core with one 100x80 output OUT-1.
func captureCore(t *testing.T) *captureRig {
	t.Helper()
	cfg := scrollDefaults()
	cfg.Border.Width = 0
	r := &captureRig{
		client:     make(chan ports.ClientEvent, 16),
		output:     make(chan ports.OutputEvent, 8),
		commands:   make(chan ports.ClientCommand, 64),
		scenes:     make(chan []ports.Scene, 1),
		workspaces: make(chan ports.Workspaces, 1),
	}
	c, err := core.New(cfg, core.Channels{Client: r.client, Output: r.output, Commands: r.commands, Scenes: r.scenes, Workspaces: r.workspaces}, core.Options{})
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

// excludedScene waits for a scene whose exclusion list has n entries.
func (r *captureRig) excludedScene(t *testing.T, n int) ports.Scene {
	t.Helper()
	for {
		if s := r.sceneWith(t, true); len(s.Capture.Excluded) == n {
			return s
		}
	}
}

func TestCaptureSessionOutputTarget(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1", Region: ports.Rect{X: -10, Y: 10, W: 50, H: 200}}
	st := r.state(t)
	if !st.Active || st.Hidden || st.Workspace != 0 || st.Output != "OUT-1" || st.Rect != (ports.Rect{X: 0, Y: 10, W: 40, H: 70}) {
		t.Fatalf("state %+v", st)
	}
	// Without an exclusion the scene carries no exclusion and no hidden workspace.
	if s := r.sceneWith(t, true); s.Capture.Session != 0 || len(s.Capture.Excluded) != 0 || s.Capture.Workspace != 0 || s.CaptureScene != nil {
		t.Fatalf("capture %+v", s.Capture)
	}
	r.client <- ports.CaptureSessionClose{ID: 1}
	r.sceneWith(t, false)
	// A zero region is the whole output.
	r.client <- ports.CaptureSessionOpen{ID: 2, Output: "OUT-1"}
	if st := r.state(t); st.Rect != (ports.Rect{W: 100, H: 80}) {
		t.Fatalf("state %+v", st)
	}
}

func TestCaptureSessionWorkspaceTarget(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	ws := receive(t, r.workspaces).Outputs[0].Workspaces
	if ws[0].Frame != (ports.Rect{W: 100, H: 80}) {
		t.Fatalf("frame %+v", ws[0])
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Workspace: ws[0].ID}
	if st := r.state(t); !st.Active || st.Hidden || st.Output != "OUT-1" || st.Workspace != ws[0].ID || st.Rect != ws[0].Frame {
		t.Fatalf("state %+v", st)
	}
	if s := r.sceneWith(t, true); s.Capture.Shown != ws[0].ID || s.Capture.Workspace != 0 {
		t.Fatalf("capture %+v", s.Capture)
	}
	r.client <- ports.CaptureSessionClose{ID: 1}
	r.sceneWith(t, false)
	// A workspace that does not exist ends the session.
	r.client <- ports.CaptureSessionOpen{ID: 2, Workspace: 9999}
	if st := r.state(t); st.Active || st.Reason != ports.CaptureReasonWorkspaceGone || !st.Reason.Terminal() {
		t.Fatalf("state %+v", st)
	}
}

// Only one hidden workspace is rendered at a time: a second session on
// another hidden workspace is not Active until the first one ends.
func TestCaptureSessionOneHiddenWorkspaceAtATime(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	var ws []ports.WorkspaceInfo
	for len(ws) < 2 {
		ws = receive(t, r.workspaces).Outputs[0].Workspaces
	}
	// Workspace 2 is on screen after this; 1 and a third are hidden.
	r.client <- ports.WorkspaceActivate{IDs: []uint64{ws[1].ID}}
	r.client <- ports.CaptureSessionOpen{ID: 1, Workspace: ws[0].ID}
	if st := r.state(t); !st.Active || !st.Hidden {
		t.Fatalf("first hidden session %+v", st)
	}
	r.client <- ports.CaptureSessionOpen{ID: 2, Workspace: ws[0].ID}
	r.client <- ports.CaptureSessionOpen{ID: 3, Workspace: ws[1].ID}
	for {
		if st := r.state(t); st.ID == 3 {
			if !st.Active || st.Hidden {
				t.Fatalf("session on the workspace on screen %+v", st)
			}
			break
		}
	}
}

func TestCaptureSessionTerminalReasons(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "NOPE"}
	if st := r.state(t); st.Reason != ports.CaptureReasonOutputGone || st.Active {
		t.Fatalf("state %+v", st)
	}
	r.client <- ports.CaptureSessionOpen{ID: 2, Output: "OUT-1", Region: ports.Rect{X: 500, Y: 500, W: 5, H: 5}}
	if st := r.state(t); st.Reason != ports.CaptureReasonInvalidRegion {
		t.Fatalf("state %+v", st)
	}
	// An unplugged output ends the session.
	r.client <- ports.CaptureSessionOpen{ID: 3, Output: "OUT-1"}
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

// Several sessions live at once; each is decided on its own.
func TestCaptureSessionsAreIndependent(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionOpen{ID: 2, Output: "OUT-1", Region: ports.Rect{W: 10, H: 10}}
	got := map[uint64]ports.CaptureSessionState{}
	for len(got) < 2 {
		st := r.state(t)
		got[st.ID] = st
	}
	if !got[1].Active || !got[2].Active || got[2].Rect != (ports.Rect{W: 10, H: 10}) {
		t.Fatalf("states %+v", got)
	}
	r.client <- ports.CaptureSessionClose{ID: 1}
	r.client <- ports.CaptureSessionClose{ID: 7} // unknown: ignored
	r.client <- ports.WindowMapped{ID: 1}
	r.sceneWith(t, true) // session 2 lives on
}

// An exclusion needs a live session; attached layers stay over a fullscreen
// window, are excluded with their popups, and other layers keep hiding.
func TestCaptureExclusionLayersOverFullscreen(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	scene(t, r.scenes)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0), r.layer(11, ports.LayerTop, 0)}}
	if s := scene(t, r.scenes); len(s.Layers) != 0 {
		t.Fatalf("fullscreen should hide layers: %+v", s.Layers)
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true} // duplicate
	r.client <- ports.PopupRequest{ID: 20, Parent: 10, Positioner: ports.Positioner{Width: 5, Height: 5, AnchorRect: ports.Rect{W: 1, H: 1}}}
	r.client <- ports.PopupMapped{ID: 20}
	s := r.excludedScene(t, 2)
	if s.Capture.Session != 1 || s.Capture.Excluded[0] != 10 || s.Capture.Excluded[1] != 20 {
		t.Fatalf("capture %+v", s.Capture)
	}
	if len(s.Layers) != 1 || s.Layers[0].ID != 10 {
		t.Fatalf("only the attached layer shows over fullscreen: %+v", s.Layers)
	}
	// The exclusion ends with its session.
	r.client <- ports.CaptureSessionClose{ID: 1}
	if s = r.sceneWith(t, false); len(s.Layers) != 0 {
		t.Fatalf("layers after the session: %+v", s.Layers)
	}
}

// The exclusion of a session that is not registered, or of another session
// than the live one, does nothing.
func TestCaptureExclusionNeedsItsSession(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureExclusionBegin{Session: 5} // unknown session
	r.client <- ports.CaptureExclusionLayer{Session: 5, Layer: 10, Attached: true}
	r.client <- ports.WindowMapped{ID: 1}
	if s := scene(t, r.scenes); s.Capture != nil {
		t.Fatalf("capture without session: %+v", s.Capture)
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureSessionOpen{ID: 2, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionBegin{Session: 2}                            // only one is live
	r.client <- ports.CaptureExclusionLayer{Session: 2, Layer: 10, Attached: true} // not the owner
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 11, Attached: true}
	if s := r.excludedScene(t, 1); s.Capture.Session != 1 || s.Capture.Excluded[0] != 11 {
		t.Fatalf("capture %+v", s.Capture)
	}
}

// Wayland ends an exclusion whose object was destroyed; the session goes on.
func TestCaptureExclusionEndKeepsSession(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.excludedScene(t, 1)
	r.client <- ports.CaptureExclusionEnd{Session: 1}
	for {
		s := r.sceneWith(t, true)
		if s.Capture.Session == 0 && len(s.Capture.Excluded) == 0 {
			break
		}
	}
	// A new exclusion can begin on the live session.
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 12, Attached: true}
	if s := r.excludedScene(t, 1); s.Capture.Excluded[0] != 12 {
		t.Fatalf("capture %+v", s.Capture)
	}
}

// When the exclusion object is destroyed and the session goes on, its HUD
// layers that a screen still lists stay excluded (and are no longer kept over
// a fullscreen window) until they are not listed: the recorder's frames never
// show the HUD after the exclusion ended.
func TestCaptureExclusionEndKeepsListedLayersExcluded(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	scene(t, r.scenes)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	s := r.excludedScene(t, 1)
	if len(s.Layers) != 1 {
		t.Fatalf("attached layer shows over fullscreen: %+v", s.Layers)
	}
	before := s.Capture.Revision
	r.client <- ports.CaptureExclusionEnd{Session: 1}
	s = r.excludedScene(t, 1)
	if s.Capture.Session != 1 || s.Capture.Excluded[0] != 10 || s.Capture.Revision <= before {
		t.Fatalf("ended exclusion's layer must stay excluded and fenced: %+v", s.Capture)
	}
	if len(s.Layers) != 0 {
		t.Fatalf("an ended exclusion keeps nothing over fullscreen: %+v", s.Layers)
	}
	st := r.state(t)
	for st.Revision != s.Capture.Revision {
		st = r.state(t)
	}
	if !st.Exclusion || len(st.Layers) != 0 {
		t.Fatalf("state %+v", st)
	}
	// The layer goes: nothing is excluded any more and the fence is lifted.
	r.client <- ports.LayerChanged{}
	for {
		s = r.sceneWith(t, true)
		if s.Capture.Session == 0 && len(s.Capture.Excluded) == 0 {
			break
		}
	}
	for st := r.state(t); st.Exclusion; st = r.state(t) {
	}
}

func TestCaptureExclusionLayerBounds(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	for id := ports.WindowID(1); id <= ports.MaxExclusionLayers+3; id++ {
		r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: id, Attached: true}
	}
	s := r.excludedScene(t, ports.MaxExclusionLayers)
	for _, id := range s.Capture.Excluded {
		if id > ports.MaxExclusionLayers {
			t.Fatalf("excluded %v", s.Capture.Excluded)
		}
	}
	// Detaching removes one.
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 1}
	r.excludedScene(t, ports.MaxExclusionLayers-1)
}

// More excluded surfaces than a capture can leave out (layers and popups)
// stop the session: fail closed.
func TestCaptureExclusionTooManyExcluded(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	stopped := make(chan struct{})
	go func() {
		// Drain the commands while the popups are sent.
		for cmd := range r.commands {
			if st, ok := cmd.(ports.CaptureSessionState); ok && st.Reason == ports.CaptureReasonTooManyExcluded {
				close(stopped)
				return
			}
		}
	}()
	for i := range ports.MaxCaptureExcluded {
		r.client <- ports.PopupRequest{ID: ports.WindowID(100 + i), Parent: 10, Positioner: ports.Positioner{Width: 5, Height: 5, AnchorRect: ports.Rect{W: 1, H: 1}}}
		r.client <- ports.PopupMapped{ID: ports.WindowID(100 + i)}
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("session not stopped")
	}
}

// An attached layer asking for exclusive keyboard never gets it; an
// unrelated layer may.
func TestCaptureExclusionKeyboardClamp(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0), r.layer(11, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.excludedScene(t, 1)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 1), r.layer(11, ports.LayerOverlay, 1)}}
	for len(r.commands) > 0 {
		<-r.commands
	}
	scene(t, r.scenes)
	for {
		if f, ok := receive(t, r.commands).(ports.FocusWindow); ok {
			if f.ID != 11 {
				t.Fatalf("keyboard went to %d, want the unattached layer 11", f.ID)
			}
			return
		}
	}
}

// An attached layer never gets keyboard interactivity, exclusive or on demand.
func TestCaptureExclusionKeyboardNone(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 2), r.layer(11, ports.LayerOverlay, 2)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.excludedScene(t, 1)
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

// Every change an excluded frame depends on bumps the revision, in the state
// and in the scene that carries it; an unchanged republish does not.
func TestCaptureExclusionRevisionFence(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	if st := r.state(t); st.Exclusion || st.Revision != 0 {
		t.Fatalf("state without exclusion %+v", st)
	}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	st := r.state(t)
	if !st.Exclusion || st.Revision != 1 || len(st.Layers) != 0 {
		t.Fatalf("state %+v", st)
	}
	for {
		if s := r.sceneWith(t, true); s.Capture.Revision == 1 {
			break
		}
	}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
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
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10}
	if st = r.state(t); st.Revision != 3 || len(st.Layers) != 0 {
		t.Fatalf("state after detach %+v", st)
	}
}

// A detached layer stays excluded until a LayerChanged stops listing it, so
// the last frame of a destroyed HUD never leaks into a capture.
func TestCaptureExclusionDetachRetains(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.excludedScene(t, 1)
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10}
	if s := r.sceneWith(t, true); len(s.Capture.Excluded) != 1 || s.Capture.Excluded[0] != 10 {
		t.Fatalf("detached layer no longer excluded while mapped: %+v", s.Capture)
	}
	r.client <- ports.LayerChanged{Layers: nil}
	r.excludedScene(t, 0)
}

// An unmap queued before the detach (the wayland order) never leaves a
// window where the layer is listed but not excluded.
func TestCaptureExclusionUnmapThenDetach(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
	r.client <- ports.CaptureExclusionBegin{Session: 1}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
	r.excludedScene(t, 1)
	r.client <- ports.LayerChanged{Layers: nil}
	r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10}
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

// A captured frame shows the indicator for one second on the clock core
// reads, then the scene goes back without it, whoever asked for the frame.
func TestCaptureFrameFlashRunsOnTheCoreClock(t *testing.T) {
	cfg := scrollDefaults()
	cfg.Border.Width = 0
	fire := make(chan time.Time)
	timer := portsmocks.NewMockTimer(t)
	timer.EXPECT().C().Return((<-chan time.Time)(fire))
	timer.EXPECT().Stop().Return(true).Maybe()
	var now atomic.Int64
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time { return time.Unix(now.Load(), 0) }).Maybe()
	clock.EXPECT().NewTimer(ports.CaptureFlash).Return(timer).Once()
	client := make(chan ports.ClientEvent, 4)
	output := make(chan ports.OutputEvent, 4)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Output: output, Commands: make(chan ports.ClientCommand, 64), Scenes: scenes}, core.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	// A wlr-screencopy frame: no session.
	client <- ports.CaptureFrameTaken{Output: "OUT-1", Region: ports.Rect{X: 10, Y: 10, W: 20, H: 20}}
	if s := scene(t, scenes); len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0].Rect != (ports.Rect{X: 10, Y: 10, W: 20, H: 20}) || s.CaptureIndicators[0].Pill {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
	now.Store(1)
	fire <- time.Time{}
	if s := scene(t, scenes); s.CaptureIndicators != nil {
		t.Fatalf("indicators %+v after the flash", s.CaptureIndicators)
	}
}

// popupIDs lists the popups of a scene.
func popupIDs(s ports.Scene) []ports.WindowID {
	var ids []ports.WindowID
	for _, w := range s.Windows {
		if w.Popup {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

// A popup of a HUD layer is excluded for as long as it is in the scene, from
// the session's end to the layer's: when the session closes, or the layer is
// destroyed, the popup is never left in the scene while no longer excluded.
func TestCaptureExclusionPopupOfHUDLayerNeverLeaksWhenTheExclusionEnds(t *testing.T) {
	setup := func(t *testing.T) *captureRig {
		r := captureCore(t)
		r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{r.layer(10, ports.LayerOverlay, 0)}}
		r.client <- ports.CaptureSessionOpen{ID: 1, Output: "OUT-1"}
		r.client <- ports.CaptureExclusionBegin{Session: 1}
		r.client <- ports.CaptureExclusionLayer{Session: 1, Layer: 10, Attached: true}
		r.client <- ports.PopupRequest{ID: 20, Parent: 10, Positioner: ports.Positioner{Width: 5, Height: 5, AnchorRect: ports.Rect{W: 1, H: 1}}}
		r.client <- ports.PopupMapped{ID: 20}
		for {
			s := r.excludedScene(t, 2)
			if ids := popupIDs(s); len(ids) == 1 && ids[0] == 20 {
				break
			}
		}
		return r
	}
	// leaks: a scene that draws the popup without excluding it, while a
	// session that could still capture it with the exclusion is alive.
	t.Run("exclusion object destroyed, session lives, layer goes away", func(t *testing.T) {
		r := setup(t)
		r.client <- ports.CaptureExclusionEnd{Session: 1}
		// The layer is still listed: it and its popup stay excluded.
		s := r.excludedScene(t, 2)
		if ids := popupIDs(s); len(ids) != 1 || !slices.Contains(s.Capture.Excluded, 20) {
			t.Fatalf("popup %v of a retained HUD layer not excluded: %+v", ids, s.Capture)
		}
		r.client <- ports.LayerChanged{} // the layer is destroyed
		for {
			s := scene(t, r.scenes)
			if len(s.Layers) != 0 {
				continue
			}
			for _, w := range s.Windows {
				if w.ID == 20 && (s.Capture == nil || !slices.Contains(s.Capture.Excluded, 20)) {
					t.Fatalf("popup of a destroyed layer stays in the scene, unexcluded: %+v", s.Capture)
				}
			}
			break
		}
	})
	t.Run("session closed", func(t *testing.T) {
		r := setup(t)
		r.client <- ports.CaptureSessionClose{ID: 1}
		s := r.sceneWith(t, false)
		if ids := popupIDs(s); len(ids) != 0 && len(s.Layers) == 0 {
			t.Fatalf("popup %v outlived its HUD layer's exclusion while the layer is hidden", ids)
		}
	})
}
