package core

import (
	"context"
	"reflect"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// Capture state (ports/capture.go). Only the core goroutine touches it.
//
// Things that live here:
//   - the registered ext-image-copy-capture sessions, decided on every publish
//     (target, hidden workspace, end);
//   - the one exclusion, whose HUD layers stay visible over a fullscreen
//     window and are left out of its session's frames;
//   - the workspace rendered off screen for a session (Scene.CaptureScene).
//
// With no session and no exclusion none of this allocates: the scenes carry
// no Capture.

// capTarget is what a capture covers, as asked.
type capTarget struct {
	output    string
	workspace uint64
	rect      Rect // output-local logical; zero is the whole output or frame
}

type capSession struct {
	open    ports.CaptureSessionOpen
	last    ports.CaptureSessionState
	hasLast bool
}

func (s *capSession) target() capTarget {
	return capTarget{output: s.open.Output, workspace: s.open.Workspace, rect: s.open.Region}
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
	excluded  []WindowID
	exclusion *capExclusion
}

// capResolved is a target located on the current screens.
type capResolved struct {
	sc     *screen
	ws     *Workspace
	rect   Rect // clipped, output-local logical
	hidden bool
}

// capResolve locates a target. A zero reason is success.
func (c *Core) capResolve(t capTarget) (capResolved, ports.CaptureReason) {
	var r capResolved
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
	c.capSessions = append(c.capSessions, &capSession{open: v})
}

func (c *Core) capSession(id uint64) *capSession {
	for _, s := range c.capSessions {
		if s.open.ID == id {
			return s
		}
	}
	return nil
}

// captureClose forgets a session and its exclusion. Its flashes stay.
func (c *Core) captureClose(id uint64) {
	c.capSessions = slices.DeleteFunc(c.capSessions, func(s *capSession) bool { return s.open.ID == id })
	if c.capExcl != nil && c.capExcl.session == id {
		c.dropExclusion()
	}
}

// captureExclusionBegin starts the exclusion of a session. Wayland refused
// it already when another one was live.
func (c *Core) captureExclusionBegin(v ports.CaptureExclusionBegin) {
	if c.capSession(v.Session) == nil {
		return
	}
	if old := c.capExcl; old != nil {
		if !old.ended {
			return
		}
		// An ended exclusion still excludes its listed HUD layers: a new one
		// inherits them, so they are left out until no screen lists them.
		c.capExcl = &capExclusion{session: v.Session, retained: old.retained, rev: old.rev}
		return
	}
	c.capExcl = &capExclusion{session: v.Session}
}

// captureExclusionLayer attaches or detaches a layer surface.
func (c *Core) captureExclusionLayer(v ports.CaptureExclusionLayer) {
	e := c.capExcl
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
	e := c.capExcl
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
	if c.capExcl != nil {
		// Attached layers no longer stay over a fullscreen window.
		for _, sc := range c.screens {
			sc.capture = nil
		}
	}
}

// dropCaptureSessions forgets every session and the exclusion: the session is
// protected, no capture is served, and wayland has stopped them all.
func (c *Core) dropCaptureSessions() {
	c.capSessions = nil
	c.dropExclusion()
}

func (c *Core) dropExclusion() {
	c.capExcl = nil
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
	e := c.capExcl
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
	e := c.capExcl
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
func (c *Core) captureExcluded(e *capExclusion) []WindowID {
	out := append(slices.Clone(e.layers), e.retained...)
	for id := range c.popups {
		if root := c.popupRoot(id); slices.Contains(e.layers, root) || slices.Contains(e.retained, root) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// captureEvaluate decides every session against the current screens, sends
// the states that changed and returns the view of this publish; nil when
// there is no session and no exclusion. A terminal state
// is sent once and the session is forgotten.
func (c *Core) captureEvaluate(ctx context.Context) (*capView, error) {
	if len(c.capSessions) == 0 && c.capExcl == nil {
		return nil, nil
	}
	c.pruneRetained()
	v := &capView{}
	// Decide each session.
	states := make([]ports.CaptureSessionState, 0, len(c.capSessions))
	alive := c.capSessions[:0:0]
	for _, s := range c.capSessions {
		st := ports.CaptureSessionState{ID: s.open.ID}
		r, reason := c.capResolve(s.target())
		if reason == ports.CaptureReasonNone {
			st.Output, st.Rect, st.Workspace, st.Hidden = r.sc.name(), r.rect, s.open.Workspace, r.hidden
			if r.hidden && v.hiddenID == 0 {
				v.hidden, v.hiddenScr, v.hiddenID = r.ws, r.sc, s.open.Workspace
			}
			// One hidden workspace is rendered at a time.
			st.Active = !r.hidden || v.hiddenID == s.open.Workspace
		}
		st.Reason = reason
		states = append(states, st)
		if reason != ports.CaptureReasonNone {
			if err := c.command(ctx, st); err != nil {
				return nil, err
			}
			if c.capExcl != nil && c.capExcl.session == s.open.ID {
				c.dropExclusion()
			}
			continue
		}
		alive = append(alive, s)
	}
	c.capSessions = alive
	// Exclusion.
	if e := c.capExcl; e != nil {
		ex := c.captureExcluded(e)
		if len(ex) > ports.MaxCaptureExcluded {
			// Fail closed: the session cannot leave them all out.
			st := ports.CaptureSessionState{ID: e.session, Reason: ports.CaptureReasonTooManyExcluded}
			if err := c.command(ctx, st); err != nil {
				return nil, err
			}
			c.capSessions = slices.DeleteFunc(c.capSessions, func(s *capSession) bool { return s.open.ID == e.session })
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
			key := capExclusionKey{excluded: ex, layers: slices.Clone(e.layers), target: st}
			if !e.hasKey || !reflect.DeepEqual(e.key, key) {
				e.rev++
			}
			e.key, e.hasKey = key, true
		}
	}
	// Tell wayland what changed.
	for _, s := range c.capSessions {
		var st ports.CaptureSessionState
		for _, x := range states {
			if x.ID == s.open.ID {
				st = x
			}
		}
		if e := c.capExcl; e != nil && e.session == s.open.ID {
			st.Exclusion, st.Revision, st.Layers = true, e.rev, slices.Clone(e.layers)
		}
		if !s.hasLast || !reflect.DeepEqual(s.last, st) {
			s.last, s.hasLast = st, true
			if err := c.command(ctx, st); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// captureSceneFor is the capture state of one screen's scene; nil when
// there is no session. Shown is the workspace drawn as itself on screen.
func (c *Core) captureSceneFor(sc *screen, v *capView) *ports.SceneCapture {
	if v == nil || len(c.capSessions) == 0 && v.exclusion == nil {
		return nil
	}
	sceneCap := &ports.SceneCapture{}
	if !sc.mon.ov.open {
		sceneCap.Shown = sc.mon.Current().ID
	}
	if e := v.exclusion; e != nil {
		sceneCap.Session, sceneCap.Revision, sceneCap.Excluded = e.session, e.rev, v.excluded
	}
	if v.hiddenScr == sc {
		sceneCap.Workspace = v.hiddenID
	}
	return sceneCap
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
