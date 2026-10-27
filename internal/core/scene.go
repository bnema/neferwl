package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// sceneFor builds the scene of screen i and sends the configures it sizes.
func (c *Core) sceneFor(ctx context.Context, i int, sc *screen, only *screen, capture *capView) (scene ports.Scene, err error) {
	o := sc.mon.Output()
	// view.frame is the viewport of the workspace on screen: the whole
	// output unless it has a size override (never in the overview).
	view := sc.mon.viewport()
	var clip Rect
	if view.frame != (Rect{W: o.W, H: o.H}) {
		clip = view.frame
	}
	// layout is what is drawn; settled (same indexes) is where the
	// windows are going, and alone sizes the configures.
	layout, settled := sc.shown, sc.settledLayout
	scene = ports.Scene{Security: c.security, Output: sc.name(), OutputWidth: o.W, OutputHeight: o.H, WorkspaceClip: clip, Scale: sc.scale, Transform: sc.transform, Off: sc.off, Background: c.cfg.Background.Color, Border: ports.Border{Radius: c.cfg.Border.Radius, Width: c.cfg.Border.Width, Active: c.cfg.Border.Active, Inactive: c.cfg.Border.Inactive}, Windows: make([]ports.SceneWindow, 0, len(layout)+len(c.popupOrder)+1), Layers: shownLayers(sc)}
	if !sc.mon.previewing() {
		// Tiles stop at the panels; the overview lays out previews over
		// the whole output.
		u := view.usable
		scene.TileInset = ports.Insets{Top: u.Y, Left: u.X, Right: o.W - u.X - u.W, Bottom: o.H - u.Y - u.H}
	}
	var real map[WindowID]Placement
	if sc.mon.previewing() {
		// Local to this screen's build: reused, cleared each time.
		if c.overviewReal == nil {
			c.overviewReal = make(map[WindowID]Placement)
		}
		clear(c.overviewReal)
		real = c.overviewReal
		for w := range sc.mon.all() {
			c.realBuf = w.AppendLayout(c.realBuf[:0])
			for _, p := range c.realBuf {
				real[p.ID] = p
			}
		}
	}
	scene.Dim = floatDim(layout, view, c.cfg.Floating.Dim)
	if sc.mon.previewing() {
		// Darken the wallpaper around the previews.
		scene.Dim = c.cfg.Floating.Dim
		scene.DimBehind = scene.Dim > 0
	}
	if d := c.drag; d != nil && d.target.screen == sc && d.target.kind != DropNone {
		scene.DropHints = slices.Clone(d.target.hints)
	}
	if sc.mon.ov.open {
		// Frame the selection and separate numbered and named row groups.
		// The scene's own slice (immutable once sent), sized for both.
		_, dividers := sc.mon.overviewRows()
		var seps []ports.Separator
		if len(dividers) > 0 {
			seps = append(make([]ports.Separator, 0, len(dividers)+outlineLines), dividers...)
		}
		scene.Separators, scene.Outline = overviewOutline(seps, layout, max(c.cfg.Border.Width, 2), sc.mon.Current().Gap(), c.cfg.Border.Radius)
	} else if sc.mon.switcherShown() {
		// The switcher's cards: only the selection is framed.
		scene.Separators, scene.Outline = overviewOutline(nil, layout, max(c.cfg.Border.Width, 2), sc.mon.Current().Gap(), c.cfg.Border.Radius)
	} else {
		// Only the focused output lights the focused window's lines.
		scene.Separators = separators(layout, c.cfg.Border.Width, sc.mon.Current().Gap(), view, i == c.focusScreen)
	}
	for k, p := range layout {
		ps := settled[k]
		// Only the focused output has an activated window.
		focused := p.Focused && i == c.focusScreen
		sw := ports.SceneWindow{ID: p.ID, Rect: p.Rect, Focused: focused, Fullscreen: p.Fullscreen, Hidden: p.Hidden, Floating: p.Floating, Below: p.Below, Inset: p.Inset, Preview: p.Preview, Fade: p.Fade}
		sw.Radius = windowRadius(p, sc.mon.Current().Gap(), c.cfg.Border.Radius)
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
		scene.Windows = append(scene.Windows, sw)
		t := configureTarget{output: sc.name(), view: view, focused: focused, captured: capture != nil && capture.window == p.ID}
		if !p.Hidden && p.Preview == 0 {
			// Only a sized configure needs the client size.
			t.client, t.imposed = c.clientRect(ps), sc.mon.Current().ImposedFloat(p.ID)
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
				return ports.Scene{}, err
			}
			c.configures.mark(v)
		}
	}
	scene.Windows = append(scene.Windows, c.scenePopups(sc)...)
	if icon, ok := c.dataDragIconOn(i, sc); ok {
		// The drag icon is over everything, popups and layers included.
		scene.Windows = append(scene.Windows, icon)
	}
	scene.PositionHint = sc.positionHint.sample
	scene.Flash = c.flashFor(sc)
	scene.CaptureIndicators = c.captureIndicators(sc)
	scene.Overlays = c.hintOverlays(sc)
	if c.osd.shown {
		// Only where the target is drawn: an overlay nothing draws would
		// still refuse direct scanout on this output.
		if osdDrawn(scene, c.osd.window) {
			scene.Overlays = append(scene.Overlays, ports.Overlay{Window: c.osd.window, ID: overlayIDOSD, Seq: c.osd.seq, Kind: ports.OverlayPanel, Anchor: ports.AnchorTopCenter, Rect: ports.Rect{Y: 12}, Style: osdStyle, Panel: c.osd.panel})
		}
	}
	scene.Physical = ports.OutputPhysical{Width: sc.info.Width, Height: sc.info.Height, MMWidth: sc.info.PhysicalW, MMHeight: sc.info.PhysicalH}
	scene.Capture = c.captureSceneFor(sc, capture)
	// The springs of this frame are advanced: a scene shown while one still
	// runs wakes core when it flips; the one a spring settles on does not.
	scene.Animating = c.sliding(sc.name())
	// A scene carrying a capture image always gets a fresh Seq, and so
	// does one of an animating screen (the one being stepped, or any
	// when none is): the outputs drop a scene they already show, and a
	// spring that starts at the previous scene, or rounds to it in its
	// tail, would get no flip and wait for the fallback timer. So does the
	// scene that flips Animating off: SameAs ignores the flag, and keeping
	// the Seq of the animating scene would leave outputs showing it, and
	// waking core on every later flip. Any other keeps its output's Seq
	// while it draws the same, so an idle output is not recomposed.
	withCapture := scene.Capture != nil && (sc == capture.hiddenScr || sc == capture.windowScr)
	animating := scene.Animating && (only == nil || only == sc)
	if !withCapture && !animating && sc.last.Animating == scene.Animating && sc.last.Seq != 0 && scene.SameAs(sc.last) {
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
	return scene, nil
}
