package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A window session renders the window alone, at its client size, from the
// first frame and whatever workspace is shown; once the window leaves the
// screen it is configured Captured, and the session ends when it goes.
func TestCaptureWindowSession(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.WindowMapped{ID: 2}
	scene(t, r.scenes)
	var ws []ports.WorkspaceInfo
	for len(ws) < 2 {
		ws = receive(t, r.workspaces).Outputs[0].Workspaces
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Window: 1}
	st := r.state(t)
	if !st.Active || !st.Hidden || st.Output != "OUT-1" || st.Rect.W <= 0 || st.Rect.H <= 0 || st.Rect.X != 0 || st.Rect.Y != 0 {
		t.Fatalf("state %+v", st)
	}
	s := r.sceneWith(t, true)
	if s.Capture.Window != 1 || s.Capture.Workspace != 0 || s.CaptureScene == nil {
		t.Fatalf("capture %+v scene %v", s.Capture, s.CaptureScene)
	}
	c := s.CaptureScene
	if c.OutputWidth != st.Rect.W || c.OutputHeight != st.Rect.H || len(c.Windows) != 1 || c.Windows[0].ID != 1 || c.Windows[0].Rect != (ports.Rect{W: st.Rect.W, H: st.Rect.H}) {
		t.Fatalf("window scene %+v", c)
	}

	// Another workspace on screen: window 1 is hidden yet kept drawing.
	r.client <- ports.WorkspaceActivate{IDs: []uint64{ws[1].ID}}
	for {
		v := r.configureOf(t, 1)
		if !v.Visible {
			if !v.Captured || v.Width != st.Rect.W {
				t.Fatalf("hidden captured window %+v", v)
			}
			break
		}
	}
	for {
		if s = scene(t, r.scenes); s.Capture != nil && s.Capture.Window == 1 && s.CaptureScene != nil {
			break
		}
	}

	// The window goes: the session ends with window-gone.
	r.client <- ports.WindowUnmapped{ID: 1}
	for {
		if st = r.state(t); st.Reason != ports.CaptureReasonNone {
			break
		}
	}
	if st.Reason != ports.CaptureReasonWindowGone {
		t.Fatalf("end %+v", st)
	}
}

// The indicator of a window session is a pill on its output, and the
// window's outline while it is on screen.
func TestCaptureWindowIndicator(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.CaptureSessionOpen{ID: 1, Window: 1}
	r.state(t)
	r.client <- ports.CaptureFrameTaken{Session: 1, Window: 1}
	for {
		s := scene(t, r.scenes)
		pill, border := false, false
		for _, m := range s.CaptureIndicators {
			pill = pill || m.Pill
			border = border || !m.Pill
		}
		if pill && border {
			return
		}
	}
}

// A captured window on a hidden workspace follows that workspace's layout:
// when a sibling closes, the capture and its configure grow together.
func TestCaptureWindowHiddenReflow(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	r.client <- ports.WindowMapped{ID: 2}
	scene(t, r.scenes)
	var ws []ports.WorkspaceInfo
	for len(ws) < 2 {
		ws = receive(t, r.workspaces).Outputs[0].Workspaces
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Window: 1}
	before := r.state(t)
	r.client <- ports.WorkspaceActivate{IDs: []uint64{ws[1].ID}}
	for {
		if v := r.configureOf(t, 1); !v.Visible {
			break
		}
	}
	r.client <- ports.WindowUnmapped{ID: 2}
	var after ports.CaptureSessionState
	for {
		if after = r.state(t); after.Rect != before.Rect {
			break
		}
	}
	for {
		v := r.configureOf(t, 1)
		if v.Width == after.Rect.W && v.Height == after.Rect.H {
			if v.Visible || !v.Captured {
				t.Fatalf("reflowed hidden window %+v", v)
			}
			return
		}
	}
}
