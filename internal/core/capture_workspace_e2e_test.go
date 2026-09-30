package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// configureOf returns the next ConfigureWindow of id, skipping other commands.
func (r *captureRig) configureOf(t *testing.T, id ports.WindowID) ports.ConfigureWindow {
	t.Helper()
	for {
		if v, ok := receive(t, r.commands).(ports.ConfigureWindow); ok && v.ID == id {
			return v
		}
	}
}

// A session keeps capturing a workspace that leaves the screen: the state
// stays active, the scene carries a sibling CaptureScene, and the window is
// configured Captured (sized, physically invisible) until the session stops.
func TestCaptureSessionSurvivesWorkspaceSwitch(t *testing.T) {
	r := captureCore(t)
	r.client <- ports.WindowMapped{ID: 1}
	scene(t, r.scenes)
	// Mapping the window adds a spare workspace: wait for the inventory that lists it.
	var ws []ports.WorkspaceInfo
	for len(ws) < 2 {
		ws = receive(t, r.workspaces).Outputs[0].Workspaces
	}
	r.client <- ports.CaptureSessionOpen{ID: 1, Workspace: ws[0].ID}
	if st := r.state(t); !st.Active {
		t.Fatalf("state %+v", st)
	}
	on := r.sceneWith(t, true)
	if on.CaptureScene != nil {
		t.Fatalf("a workspace on screen needs no capture scene: %+v", on.CaptureScene)
	}
	// The window is shown and not Captured until its workspace leaves.
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.ConfigureWindow); ok && v.ID == 1 && (!v.Visible || v.Captured) {
			t.Fatalf("shown window %+v", v)
		}
	}

	r.client <- ports.WorkspaceActivate{IDs: []uint64{ws[1].ID}}
	var off ports.Scene
	for {
		off = scene(t, r.scenes)
		if off.CaptureScene != nil {
			break
		}
	}
	if off.Capture.Workspace != ws[0].ID {
		t.Fatalf("capture %+v", off.Capture)
	}
	c := off.CaptureScene
	if c.Capture != nil || c.CaptureScene != nil || len(c.Layers) != 0 || c.Seq != off.Seq || c.OutputWidth != 100 || c.OutputHeight != 80 {
		t.Fatalf("capture scene %+v (root seq %d)", c, off.Seq)
	}
	if len(c.Windows) != 1 || c.Windows[0].ID != 1 || c.Windows[0].Hidden || c.Windows[0].Rect.W <= 0 {
		t.Fatalf("capture windows %+v", c.Windows)
	}
	for _, w := range off.Windows {
		if w.ID == 1 && !w.Hidden {
			t.Fatalf("captured window drawn on screen: %+v", w)
		}
	}
	hid := r.configureOf(t, 1)
	if hid.Visible || hid.Activated || !hid.Captured || hid.Width <= 0 {
		t.Fatalf("captured hidden window %+v", hid)
	}

	// Stopping the session hides the window for real again.
	r.client <- ports.CaptureSessionClose{ID: 1}
	var after ports.Scene
	for {
		if after = scene(t, r.scenes); after.Capture == nil {
			break
		}
	}
	end := r.configureOf(t, 1)
	if end.Captured || end.Visible || end.Width != hid.Width {
		t.Fatalf("released window %+v", end)
	}
}
