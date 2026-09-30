package core

import (
	"context"
	"reflect"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// captureSession is the one live private capture session (ports/capture_session.go).
// Core owns its state; wayland only relays requests and decisions.
type captureSession struct {
	begin  ports.CaptureSessionBegin
	layers []WindowID // attached layer surfaces, at most ports.MaxCaptureSessionLayers
	// retained are detached layer surfaces still listed by a screen: they
	// stay excluded until a LayerChanged stops listing them, so a HUD that
	// is destroyed is never captured during its last frame.
	retained []WindowID
	expired  bool   // no ping within ports.CaptureSessionTimeout
	rev      uint64 // revision fence, bumped when the decision changes
	last     captureView
	hasLast  bool
}

// captureView is what one evaluation of the session decided.
type captureView struct {
	state  ports.CaptureSessionState
	output string
	scene  *ports.SceneCapture // nil unless Active
	// hidden is set when the targeted workspace is not drawn as itself on
	// screen (another one is, or the overview previews it): it is rendered
	// for capture only (Scene.CaptureScene) and the physical scene carries no
	// border for it.
	hidden bool
}

// captureBegin registers a session, or refuses it as busy: the live session
// is not disturbed.
func (c *Core) captureBegin(ctx context.Context, v ports.CaptureSessionBegin) error {
	if c.capture != nil {
		return c.command(ctx, ports.CaptureSessionState{ID: v.ID, Output: v.Output, Workspace: v.Workspace, Reason: ports.CaptureReasonBusy})
	}
	c.capture = &captureSession{begin: v}
	c.armCapture()
	return nil
}

// captureLayer attaches or detaches a layer surface of the live session.
func (c *Core) captureLayer(v ports.CaptureSessionLayer) {
	s := c.capture
	if s == nil || s.begin.ID != v.ID {
		return
	}
	i := slices.Index(s.layers, v.Layer)
	switch {
	case v.Attached && i < 0 && len(s.layers) < ports.MaxCaptureSessionLayers:
		s.layers = append(s.layers, v.Layer)
		c.clampCaptureKeyboard()
	case !v.Attached && i >= 0:
		s.layers = slices.Delete(s.layers, i, i+1)
		if c.layerListed(v.Layer) {
			s.retained = append(s.retained, v.Layer)
		}
	}
}

// layerListed reports whether a screen still lists the layer surface.
func (c *Core) layerListed(id WindowID) bool {
	for _, sc := range c.screens {
		for _, l := range sc.layers {
			if l.ID == id {
				return true
			}
		}
	}
	return false
}

// pruneRetained forgets the detached layers no screen lists any more.
func (c *Core) pruneRetained() {
	if c.capture != nil {
		c.capture.retained = slices.DeleteFunc(c.capture.retained, func(id WindowID) bool { return !c.layerListed(id) })
	}
}

// clampCaptureKeyboard gives every attached layer surface no keyboard
// interactivity at all, whatever it asks for (exclusive or on demand): a
// capture HUD never takes or steals the keyboard. Run whenever the layer
// list or the attached set changes.
func (c *Core) clampCaptureKeyboard() {
	if c.capture == nil {
		return
	}
	for _, sc := range c.screens {
		for i := range sc.layers {
			if sc.layers[i].Keyboard != 0 && (slices.Contains(c.capture.layers, sc.layers[i].ID) || slices.Contains(c.capture.retained, sc.layers[i].ID)) {
				sc.layers[i].Keyboard = 0
			}
		}
	}
}

// captureEnd forgets the session on an explicit stop or its owner's disconnect.
func (c *Core) captureEnd(id uint64) {
	if c.capture != nil && c.capture.begin.ID == id {
		c.dropCapture()
	}
}

// capturePing restarts the keep-alive timer.
func (c *Core) capturePing(id uint64) {
	if c.capture != nil && c.capture.begin.ID == id {
		c.armCapture()
	}
}

func (c *Core) dropCapture() {
	c.capture = nil
	c.stopCapture()
	c.configures.cw.reset()
	for _, sc := range c.screens {
		sc.capture = nil
	}
}

// armCapture (re)starts the keep-alive timer; it exists only while a session does.
func (c *Core) armCapture() {
	c.stopCapture()
	if c.ch.Clock != nil {
		t := c.ch.Clock.NewTimer(ports.CaptureSessionTimeout)
		c.captureC, c.captureStop = t.C(), t.Stop
		return
	}
	t := time.NewTimer(ports.CaptureSessionTimeout)
	c.captureC, c.captureStop = t.C, t.Stop
}

func (c *Core) stopCapture() {
	if c.captureStop != nil {
		c.captureStop()
	}
	c.captureC, c.captureStop = nil, nil
}

// captureExpired marks the session for the next publish to end it.
func (c *Core) captureExpired() {
	c.captureC, c.captureStop = nil, nil
	if c.capture != nil {
		c.capture.expired = true
	}
}

// captureEvaluate decides the state of the session against the current
// screens, sends it when it changed and returns the view; nil without a
// session. A terminal state is sent once and the session is forgotten.
func (c *Core) captureEvaluate(ctx context.Context) (*captureView, error) {
	s := c.capture
	if s == nil {
		return nil, nil
	}
	c.pruneRetained()
	view := c.captureDecide(s)
	if view.state.Reason.Terminal() {
		c.dropCapture()
		return nil, c.command(ctx, view.state)
	}
	// Screens keep the attached layers visible over a fullscreen window
	// only while the session serves captures.
	for _, sc := range c.screens {
		sc.capture = nil
	}
	if view.state.Active {
		if i := c.screenIndex(view.output); i >= 0 {
			keep := make(map[WindowID]bool, len(s.layers))
			for _, id := range s.layers {
				keep[id] = true
			}
			c.screens[i].capture = keep
		}
	}
	// The revision grows whenever what a clean capture must do changes; the
	// scene of this publish carries it, as does the state sent now.
	view.state.Layers = slices.Clone(s.layers)
	changed := !s.hasLast || !sameCapture(s.last, view)
	if changed {
		s.rev++
	}
	view.state.Revision = s.rev
	if view.scene != nil {
		view.scene.Revision = s.rev
	}
	s.last, s.hasLast = view, true
	if changed {
		if err := c.command(ctx, view.state); err != nil {
			return nil, err
		}
	}
	return &view, nil
}

// sameCapture compares two decisions, revision aside.
func sameCapture(a, b captureView) bool {
	a.state.Revision, b.state.Revision = 0, 0
	if a.scene != nil {
		s := *a.scene
		s.Revision = 0
		a.scene = &s
	}
	if b.scene != nil {
		s := *b.scene
		s.Revision = 0
		b.scene = &s
	}
	return reflect.DeepEqual(a, b)
}

// captureDecide is the pure part of captureEvaluate.
func (c *Core) captureDecide(s *captureSession) captureView {
	b := s.begin
	st := ports.CaptureSessionState{ID: b.ID, Output: b.Output, Workspace: b.Workspace}
	v := captureView{state: st, output: b.Output}
	stop := func(r ports.CaptureReason) captureView {
		v.state.Reason = r
		return v
	}
	if s.expired {
		return stop(ports.CaptureReasonPingTimeout)
	}
	i := c.screenIndex(b.Output)
	if i < 0 || b.Output == "" {
		return stop(ports.CaptureReasonOutputGone)
	}
	sc := c.screens[i]
	if sc.off {
		return stop(ports.CaptureReasonOutputOff)
	}
	m := sc.mon
	// target is the area captures cover, before the session's own region.
	target := m.Output()
	var origin Rect // the viewport origin of a hidden workspace's CaptureScene
	if b.Workspace != 0 {
		owner, w := c.workspaceByID(b.Workspace)
		if w == nil || owner != sc {
			return stop(ports.CaptureReasonWorkspaceGone)
		}
		target, origin = w.Output, Rect{X: w.Output.X, Y: w.Output.Y}
		v.hidden = m.Current() != w || m.ov.open
	}
	r := b.Region
	if r.W > 0 && r.H > 0 {
		target = intersect(target, r)
	}
	if target.W <= 0 || target.H <= 0 {
		return stop(ports.CaptureReasonInvalidRegion)
	}
	v.state.Rect = target
	excluded := c.captureExcluded(s)
	if len(excluded) > ports.MaxCaptureExcluded {
		return stop(ports.CaptureReasonTooManyExcluded)
	}
	v.state.Active = true
	scene := &ports.SceneCapture{Session: b.ID, TargetRect: target, Excluded: excluded, Workspace: b.Workspace}
	if v.hidden {
		// Rendered from Scene.CaptureScene, whose origin is the workspace
		// viewport's: TargetRect follows it.
		scene.TargetRect.X -= origin.X
		scene.TargetRect.Y -= origin.Y
	} else if b.Record {
		scene.BorderWidth, scene.BorderColor = ports.CaptureBorderWidth, ports.CaptureBorderColor
	}
	v.scene = scene
	return v
}

// captureExcluded lists the attached layers and every popup hanging from
// one of them, sorted. Layers not mapped yet are listed too: they are
// excluded from their first frame.
func (c *Core) captureExcluded(s *captureSession) []WindowID {
	out := append(slices.Clone(s.layers), s.retained...)
	for id := range c.popups {
		if root := c.popupRoot(id); slices.Contains(s.layers, root) || slices.Contains(s.retained, root) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// workspaceByID finds a workspace, numbered or hidden, on any screen.
func (c *Core) workspaceByID(id uint64) (*screen, *Workspace) {
	for _, sc := range c.screens {
		for _, w := range sc.mon.all() {
			if w.ID == id {
				return sc, w
			}
		}
	}
	return nil, nil
}
