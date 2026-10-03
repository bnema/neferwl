package core

import (
	"context"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Capture state (ports/capture.go). Only the core goroutine touches it.
//
// Things that live here:
//   - the registered ext-image-copy-capture sessions, decided on every publish
//     (target, hidden workspace, end);
//   - the one exclusion, whose HUD layers stay visible over a fullscreen
//     window and are left out of its session's frames;
//   - the workspace rendered off screen for a session (Scene.CaptureScene);
//   - the capture indicator (capindicator below): what shows on screen that
//     something is captured, whoever captures.
//
// With no session, no exclusion and no flash none of this allocates: the
// scenes carry no Capture and no CaptureIndicators.

// captureState is everything core keeps about captures. Methods on it touch
// only this state; those that need screens, layers or popups stay on Core.
type captureState struct {
	sessions []*capSession
	excl     *capExclusion // the one live exclusion, nil when none
	// flashes are the targets of captured frames still marked; timerC fires
	// at the earliest of them (capindicator.go).
	flashes   []capFlash
	timerC    <-chan time.Time
	timerStop func() bool
	// scratch, states and marks are reused by every publish.
	scratch capView
	states  []ports.CaptureSessionState
	marks   []ports.CaptureIndicator
}

func (s *captureState) session(id uint64) *capSession {
	for _, cs := range s.sessions {
		if cs.open.ID == id {
			return cs
		}
	}
	return nil
}

// idle reports whether nothing is captured or marked: scenes carry no
// capture state.
func (s *captureState) idle() bool {
	return len(s.sessions) == 0 && len(s.flashes) == 0
}

// capTarget is what a capture covers, as asked.
type capTarget struct {
	output    string
	workspace uint64
	window    WindowID
	rect      Rect // output-local logical; zero is the whole output or frame
}

type capSession struct {
	open    ports.CaptureSessionOpen
	last    ports.CaptureSessionState
	hasLast bool
	// recording: the session produced a frame. Its target shows the
	// indicator for as long as the session lives.
	recording bool
}

func (s *capSession) target() capTarget {
	return capTarget{output: s.open.Output, workspace: s.open.Workspace, window: s.open.Window, rect: s.open.Region}
}

// capExclusion is the one live exclusion.
type capExclusion struct {
	session uint64
	layers  []WindowID // attached layer surfaces, at most ports.MaxExclusionLayers
	// retained are detached layer surfaces still listed by a screen: they
	// stay excluded until a LayerChanged stops listing them, so a HUD that
	// is destroyed is never captured during its last frame.
	retained []WindowID
	// ended: the exclusion object was destroyed and the session lives on.
	// Its layers are retained only: still excluded and fenced (frames of the
	// session keep leaving them out) until no screen lists them, then the
	// exclusion is dropped. It never shows layers over a fullscreen window.
	ended bool
	// keep is the set of attached layers, shared by every screen
	// (screen.capture) and rebuilt only when the attached set changes.
	keep   map[WindowID]bool
	rev    uint64 // revision fence, bumped when what a frame must leave out changes
	key    capExclusionKey
	hasKey bool
	// scratch is the list captureExcluded builds.
	scratch []WindowID
}

// capExclusionKey is what the revision fence follows.
type capExclusionKey struct {
	excluded []WindowID
	layers   []WindowID
	target   ports.CaptureSessionState
}

// capView is what one evaluation decided, for the scenes of this publish.
type capView struct {
	hidden    *Workspace // captured off screen, nil when none
	hiddenScr *screen
	hiddenID  uint64
	// window is the window rendered off screen for a session, 0 when none.
	// One target at most, a workspace or a window, is rendered off screen.
	window    WindowID
	windowScr *screen
	windowSz  Rect // its client size, logical, at the origin
	excluded  []WindowID
	exclusion *capExclusion
}

// offscreen reports whether the view renders a target off screen.
func (v *capView) offscreen() bool { return v.hiddenID != 0 || v.window != 0 }

// capResolved is a target located on the current screens.
type capResolved struct {
	sc     *screen
	ws     *Workspace
	rect   Rect // clipped, output-local logical
	hidden bool
	// size is a window's client size at the origin; visible tells whether
	// the window is on screen, at rect.
	size    Rect
	visible bool
}

// capResolve locates a target. A zero reason is success.
func (c *Core) capResolve(t capTarget) (capResolved, ports.CaptureReason) {
	var r capResolved
	if t.window != 0 {
		sc, w := c.screenOf(t.window)
		if w == nil || sc.name() == "" {
			return r, ports.CaptureReasonWindowGone
		}
		r.sc, r.ws, r.hidden, r.size = sc, w, true, c.windowSize(w, t.window)
		if vs, vr, ok := c.windowRect(t.window); ok && vs == sc {
			r.rect, r.visible = vr, true
		}
		return r, ports.CaptureReasonNone
	}
	if t.workspace != 0 {
		sc, w := c.workspaceByID(t.workspace)
		if w == nil || sc.name() == "" {
			return r, ports.CaptureReasonWorkspaceGone
		}
		r.sc, r.ws, r.rect = sc, w, w.Output
		r.hidden = sc.mon.Current() != w || sc.mon.ov.open
		return r, ports.CaptureReasonNone
	}
	i := c.screenIndex(t.output)
	if i < 0 || t.output == "" {
		return r, ports.CaptureReasonOutputGone
	}
	r.sc = c.screens[i]
	r.rect = r.sc.mon.Output()
	if t.rect.W != 0 || t.rect.H != 0 {
		r.rect = intersect(r.rect, t.rect)
	}
	if r.rect.W <= 0 || r.rect.H <= 0 {
		return r, ports.CaptureReasonInvalidRegion
	}
	return r, ports.CaptureReasonNone
}

// captureOpen registers a session.
func (c *Core) captureOpen(v ports.CaptureSessionOpen) {
	c.capt.sessions = append(c.capt.sessions, &capSession{open: v})
}

// captureClose forgets a session and its exclusion. Its flashes stay.
func (c *Core) captureClose(id uint64) {
	c.capt.sessions = slices.DeleteFunc(c.capt.sessions, func(s *capSession) bool { return s.open.ID == id })
	if c.capt.excl != nil && c.capt.excl.session == id {
		c.dropExclusion()
	}
}

// captureExclusionBegin starts the exclusion of a session. Wayland refused
// it already when another one was live.
func (c *Core) captureExclusionBegin(v ports.CaptureExclusionBegin) {
	if c.capt.session(v.Session) == nil {
		return
	}
	if old := c.capt.excl; old != nil {
		if !old.ended {
			return
		}
		// An ended exclusion still excludes its listed HUD layers: a new one
		// inherits them, so they are left out until no screen lists them.
		c.capt.excl = &capExclusion{session: v.Session, retained: old.retained, rev: old.rev}
		return
	}
	c.capt.excl = &capExclusion{session: v.Session}
}

// captureExclusionLayer attaches or detaches a layer surface.
func (c *Core) captureExclusionLayer(v ports.CaptureExclusionLayer) {
	e := c.capt.excl
	if e == nil || e.ended || e.session != v.Session {
		return
	}
	i := slices.Index(e.layers, v.Layer)
	switch {
	case v.Attached && i < 0 && len(e.layers) < ports.MaxExclusionLayers:
		e.layers = append(e.layers, v.Layer)
		e.keep = nil
		c.clampCaptureKeyboard()
	case !v.Attached && i >= 0:
		e.layers = slices.Delete(e.layers, i, i+1)
		e.keep = nil
		if c.layerListed(v.Layer) {
			e.retained = append(e.retained, v.Layer)
		}
	}
}

// captureExclusionEnd ends the exclusion of a session that lives on. Its
// layers that a screen still lists stay excluded until none does (the
// retained set): the recorder's frames never show a HUD that just ended.
func (c *Core) captureExclusionEnd(session uint64) {
	e := c.capt.excl
	if e == nil || e.ended || e.session != session {
		return
	}
	for _, id := range e.layers {
		if c.layerListed(id) && !slices.Contains(e.retained, id) {
			e.retained = append(e.retained, id)
		}
	}
	e.layers, e.keep, e.ended = nil, nil, true
	c.pruneRetained()
	if c.capt.excl != nil {
		// Attached layers no longer stay over a fullscreen window.
		for _, sc := range c.screens {
			sc.capture = nil
		}
	}
}

// dropCaptureSessions forgets every session and the exclusion: the session is
// protected, no capture is served, and wayland has stopped them all.
func (c *Core) dropCaptureSessions() {
	c.capt.sessions = nil
	// No capture indicator outlives the lock: nothing is drawn for a capture
	// that can no longer happen, and none is left to show after the unlock.
	c.capt.flashes = nil
	c.stopCaptureTimer()
	c.dropExclusion()
}

func (c *Core) dropExclusion() {
	c.capt.excl = nil
	c.configures.cw.reset()
	for _, sc := range c.screens {
		sc.capture = nil
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

// pruneRetained forgets the detached layers no screen lists any more, and
// the ended exclusion with the last of them.
func (c *Core) pruneRetained() {
	e := c.capt.excl
	if e == nil {
		return
	}
	e.retained = slices.DeleteFunc(e.retained, func(id WindowID) bool { return !c.layerListed(id) })
	if e.ended && len(e.retained) == 0 {
		c.dropExclusion()
	}
}

// exclusionKeep is the layers of the exclusion that stay visible over a
// fullscreen window. It is built once per change of the attached set and
// shared, read-only, by every screen.
func (c *Core) exclusionKeep(e *capExclusion) map[WindowID]bool {
	if e.ended {
		return nil
	}
	if e.keep == nil {
		e.keep = make(map[WindowID]bool, len(e.layers))
		for _, id := range e.layers {
			e.keep[id] = true
		}
	}
	return e.keep
}

// clampCaptureKeyboard gives every attached layer surface no keyboard
// interactivity at all, whatever it asks for (exclusive or on demand): a
// capture HUD never takes or steals the keyboard. Run whenever the layer
// list or the attached set changes.
func (c *Core) clampCaptureKeyboard() {
	e := c.capt.excl
	if e == nil {
		return
	}
	for _, sc := range c.screens {
		for i := range sc.layers {
			if sc.layers[i].Keyboard != 0 && (slices.Contains(e.layers, sc.layers[i].ID) || slices.Contains(e.retained, sc.layers[i].ID)) {
				sc.layers[i].Keyboard = 0
			}
		}
	}
}

// excluded lists the attached layers and every popup hanging from one of
// them, sorted. Layers not mapped yet are listed too: they are excluded from
// their first frame.
// The result is scratch of the exclusion, valid until the next call.
func (c *Core) captureExcluded(e *capExclusion) []WindowID {
	out := append(append(e.scratch[:0], e.layers...), e.retained...)
	for id := range c.popups {
		if root := c.popupRoot(id); slices.Contains(e.layers, root) || slices.Contains(e.retained, root) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	e.scratch = out
	return out
}

// captureEvaluate decides every session against the current screens, sends
// the states that changed and returns the view of this publish; nil when
// there is no session and no exclusion. A terminal state
// is sent once and the session is forgotten.
func (c *Core) captureEvaluate(ctx context.Context) (*capView, error) {
	if len(c.capt.sessions) == 0 && c.capt.excl == nil {
		return nil, nil
	}
	c.pruneRetained()
	// The scratch is the view and the states of this evaluation; nothing the
	// scenes or the commands keep points into it.
	v := &c.capt.scratch
	*v = capView{}
	// Decide each session. The live ones are kept in place.
	states := c.capt.states[:0]
	alive := c.capt.sessions[:0]
	for i, s := range c.capt.sessions {
		st := ports.CaptureSessionState{ID: s.open.ID}
		r, reason := c.capResolve(s.target())
		switch {
		case reason != ports.CaptureReasonNone:
		case s.open.Window != 0:
			// A window is always rendered off screen, alone at its size.
			st.Output, st.Rect, st.Hidden = r.sc.name(), r.size, true
			if !v.offscreen() && r.size.W > 0 && r.size.H > 0 {
				v.window, v.windowScr, v.windowSz = s.open.Window, r.sc, r.size
			}
			st.Active = v.window == s.open.Window
		default:
			st.Output, st.Rect, st.Workspace, st.Hidden = r.sc.name(), r.rect, s.open.Workspace, r.hidden
			if r.hidden && !v.offscreen() {
				v.hidden, v.hiddenScr, v.hiddenID = r.ws, r.sc, s.open.Workspace
			}
			// One target is rendered off screen at a time.
			st.Active = !r.hidden || v.hiddenID == s.open.Workspace
		}
		st.Reason = reason
		states = append(states, st)
		if reason != ports.CaptureReasonNone {
			if err := c.command(ctx, st); err != nil {
				c.capt.sessions = append(alive, c.capt.sessions[i:]...)
				return nil, err
			}
			if c.capt.excl != nil && c.capt.excl.session == s.open.ID {
				c.dropExclusion()
			}
			continue
		}
		alive = append(alive, s)
	}
	clear(c.capt.sessions[len(alive):])
	c.capt.sessions = alive
	c.capt.states = states
	// Exclusion.
	if e := c.capt.excl; e != nil {
		ex := c.captureExcluded(e)
		if len(ex) > ports.MaxCaptureExcluded {
			// Fail closed: the session cannot leave them all out.
			st := ports.CaptureSessionState{ID: e.session, Reason: ports.CaptureReasonTooManyExcluded}
			if err := c.command(ctx, st); err != nil {
				return nil, err
			}
			c.capt.sessions = slices.DeleteFunc(c.capt.sessions, func(s *capSession) bool { return s.open.ID == e.session })
			c.dropExclusion()
		} else {
			v.exclusion = e
			keep := c.exclusionKeep(e)
			for _, sc := range c.screens {
				sc.capture = keep
			}
			var st ports.CaptureSessionState
			for _, x := range states {
				if x.ID == e.session {
					st = x
				}
			}
			if !e.hasKey || !slices.Equal(e.key.excluded, ex) || !slices.Equal(e.key.layers, e.layers) || !sameCaptureState(e.key.target, st) {
				e.rev++
				e.key = capExclusionKey{excluded: slices.Clone(ex), layers: slices.Clone(e.layers), target: st}
				e.hasKey = true
			}
			// What the scenes carry is the key's list, shared while it holds.
			v.excluded = e.key.excluded
		}
	}
	// Tell wayland what changed.
	for _, s := range c.capt.sessions {
		var st ports.CaptureSessionState
		for _, x := range states {
			if x.ID == s.open.ID {
				st = x
			}
		}
		if e := c.capt.excl; e != nil && e.session == s.open.ID {
			st.Exclusion, st.Revision = true, e.rev
			// The list is sent, read by another goroutine: shared, never
			// rebuilt, while the attached set is the same.
			if s.hasLast && slices.Equal(s.last.Layers, e.layers) {
				st.Layers = s.last.Layers
			} else {
				st.Layers = slices.Clone(e.layers)
			}
		}
		if !s.hasLast || !sameCaptureState(s.last, st) {
			s.last, s.hasLast = st, true
			if err := c.command(ctx, st); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// sameCaptureState reports whether two session states say the same.
func sameCaptureState(a, b ports.CaptureSessionState) bool {
	return a.ID == b.ID && a.Output == b.Output && a.Rect == b.Rect && a.Workspace == b.Workspace &&
		a.Hidden == b.Hidden && a.Active == b.Active && a.Reason == b.Reason &&
		a.Exclusion == b.Exclusion && a.Revision == b.Revision && slices.Equal(a.Layers, b.Layers)
}

// captureSceneFor is the capture state of one screen's scene; nil when
// there is no session. Shown is the workspace drawn as itself on screen.
func (c *Core) captureSceneFor(sc *screen, v *capView) *ports.SceneCapture {
	if v == nil || len(c.capt.sessions) == 0 && v.exclusion == nil {
		return nil
	}
	want := ports.SceneCapture{}
	if !sc.mon.ov.open {
		want.Shown = sc.mon.Current().ID
	}
	if e := v.exclusion; e != nil {
		want.Session, want.Revision, want.Excluded = e.session, e.rev, v.excluded
	}
	if v.hiddenScr == sc {
		want.Workspace = v.hiddenID
	}
	if v.windowScr == sc {
		want.Window = v.window
	}
	// The scene is read by the output owner and never changed: the last one
	// is shared while it says the same.
	if p := sc.capScene; p != nil && p.Shown == want.Shown && p.Session == want.Session && p.Revision == want.Revision &&
		p.Workspace == want.Workspace && p.Window == want.Window && slices.Equal(p.Excluded, want.Excluded) {
		return p
	}
	shared := want
	sc.capScene = &shared
	return sc.capScene
}

// workspaceByID finds a workspace, numbered or hidden, on any screen.
func (c *Core) workspaceByID(id uint64) (*screen, *Workspace) {
	for _, sc := range c.screens {
		for w := range sc.mon.all() {
			if w.ID == id {
				return sc, w
			}
		}
	}
	return nil, nil
}
