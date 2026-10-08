package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// sceneFor builds the scene of screen i and sends the configures it sizes.
// drawable reports that it can show the focus pulse on the focused window.
func (c *Core) sceneFor(ctx context.Context, i int, sc *screen, only *screen, capture *capView, pulse float64) (scene ports.Scene, drawable bool, err error) {
	o := sc.mon.Output()
	// frame is the viewport of the workspace on screen: the whole output
	// unless it has a size override (never in the overview).
	frame := sc.mon.Frame()
	var clip Rect
	if frame != (Rect{W: o.W, H: o.H}) {
		clip = frame
	}
	// layout is what is drawn; settled (same indexes) is where the
	// windows are going, and alone sizes the configures.
	layout, settled := sc.shown, sc.settledLayout
	scene = ports.Scene{Security: c.security, Output: sc.name(), OutputWidth: o.W, OutputHeight: o.H, WorkspaceClip: clip, Scale: sc.scale, Transform: sc.transform, Off: sc.off, Background: c.cfg.Background.Color, Border: ports.Border{Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive}, Windows: make([]ports.SceneWindow, 0, len(layout)+len(c.popupOrder)), Layers: shownLayers(sc)}
	var real map[WindowID]Placement
	if sc.mon.ov.open {
		// Local to this screen's build: reused, cleared each time.
		if c.overviewReal == nil {
			c.overviewReal = make(map[WindowID]Placement)
		}
		clear(c.overviewReal)
		real = c.overviewReal
		for w := range sc.mon.all() {
			c.realBuf = w.layoutInto(c.realBuf)
			for _, p := range c.realBuf {
				real[p.ID] = p
			}
		}
	}
	scene.Dim = floatDim(layout, frame, c.cfg.Floating.Dim)
	if sc.mon.ov.open {
		// Darken the wallpaper around the previews.
		scene.Dim = c.cfg.Floating.Dim
		scene.DimBehind = scene.Dim > 0
	}
	if d := c.drag; d != nil && d.target.screen == sc && d.target.kind != dropNone {
		scene.DropHints = slices.Clone(d.target.hints)
	}
	if sc.mon.ov.open {
		// Frame the selection and separate numbered and named row groups.
		_, scene.Separators = sc.mon.overviewRows()
		scene.Separators = append(scene.Separators, overviewOutline(layout, max(c.cfg.Border.Width, 2))...)
	} else {
		// Only the focused output lights the focused window's lines.
		scene.Separators = separators(layout, c.cfg.Border.Width, sc.mon.Current().gap(), frame, i == c.focusScreen)
	}
	// A window alone on screen needs no pulse to show it has the focus.
	// A tile under a panel (the next cascade band) does not count.
	alone := i == c.focusScreen && c.pulse.target != 0 && visibleCount(layout, sc.mon.Current().Usable) == 1
	for k, p := range layout {
		ps := settled[k]
		// Only the focused output has an activated window.
		focused := p.Focused && i == c.focusScreen
		sw := ports.SceneWindow{ID: p.ID, Rect: p.Rect, Focused: focused, Fullscreen: p.Fullscreen, Hidden: p.Hidden, Floating: p.Floating, Below: p.Below, Inset: p.Inset, Preview: p.Preview, Fade: p.Fade}
		if p.Zoom > 0 && (p.Zoom < 1 || p.Preview > 0) {
			// A scale motion: the content follows the drawn size. A
			// card in flight drawn at its size still needs Zoom 1:
			// without it the renderer would shrink it by Preview.
			sw.Zoom = p.Zoom
		}
		// A peek's veil is the configured one plus its animated offset.
		sw.Dim = max(0, min(p.Dim+c.peekDim(p), 1))
		if p.Leaving {
			// A window fading out after it closed or hid: drawn, but
			// hidden to everything else, its configures included.
			sw.Hidden = false
			scene.Windows = append(scene.Windows, sw)
			// A window the workspace still holds (a hidden stash
			// window) keeps its last configure: prune would forget it,
			// and its next one would start from nothing. A closed one
			// was forgotten at its unmap and stays so.
			if w, _ := sc.mon.find(p.ID); w != nil {
				c.configures.keep(p.ID)
			}
			continue
		}
		if focused && p.ID == c.pulse.target && !alone && !p.Fullscreen && !p.Hidden && p.Preview == 0 && !sc.mon.ov.open {
			drawable = true
			if p.ID == c.pulse.id {
				sw.FocusEffect = pulse
			}
		}
		scene.Windows = append(scene.Windows, sw)
		t := configureTarget{output: sc.name(), area: frame, focused: focused, captured: capture != nil && capture.window == p.ID}
		if !p.Hidden && p.Preview == 0 {
			// Only a sized configure needs the client size.
			t.client, t.imposed = c.clientRect(ps), sc.mon.Current().imposedFloat(p.ID)
		} else if t.captured && p.Hidden {
			// A captured hidden window is sized like its capture.
			t.client = capture.windowSz
		} else if p.Preview > 0 && !p.Hidden {
			if rp, ok := real[p.ID]; ok && !rp.Hidden {
				t.realTiled = !rp.Floating
				t.client = c.clientRect(rp)
			}
		}
		cp, t := c.captureConfigure(sc, ps, t)
		if v, send := c.configures.nextWithCapture(ps, t, cp); send {
			if err := c.command(ctx, v); err != nil {
				return ports.Scene{}, false, err
			}
			c.configures.mark(v)
		}
	}
	scene.Windows = append(scene.Windows, c.scenePopups(sc)...)
	scene.CaptureIndicators = c.captureIndicators(sc)
	scene.Capture = c.captureSceneFor(sc, capture)
	// A scene carrying a capture image always gets a fresh Seq, and so
	// does one of an animating screen (the one being stepped, or any
	// when none is): the outputs drop a scene they already show, and a
	// spring that starts at the previous scene, or rounds to it in its
	// tail, would get no flip and wait for the fallback timer. Any other
	// keeps its output's Seq while it draws the same, so an idle output
	// is not recomposed.
	withCapture := scene.Capture != nil && (sc == capture.hiddenScr || sc == capture.windowScr)
	animating := (sc.springing() || c.pulsing(sc)) && (only == nil || only == sc)
	if !withCapture && !animating && sc.last.Seq != 0 && scene.SameAs(sc.last) {
		scene.Seq = sc.last.Seq
	} else {
		c.seq++
		scene.Seq = c.seq
	}
	if withCapture {
		switch sc {
		case capture.hiddenScr:
			scene.CaptureScene = c.captureScene(scene.Seq)
		case capture.windowScr:
			scene.CaptureScene = c.captureWindowScene(capture, scene.Seq)
		}
	}
	return scene, drawable, nil
}
