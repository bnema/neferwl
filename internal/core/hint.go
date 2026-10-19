package core

import (
	"cmp"
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// Hint mode: the hint action asks the scanner (HintScans) for the clickable
// targets in the last frame of the focused output, labels the ones that sit
// over a managed window, grabs the keyboard for the label letters, and clicks
// the centre of the target whose label was typed. Everything here runs on the
// owner goroutine.

// hintAlphabet orders the label letters: home row first.
const hintAlphabet = "fjdkslaghrueiwoqptyvbcnxmz"

// hintMaxTargets is the most targets labelled: every two-letter label.
const hintMaxTargets = len(hintAlphabet) * len(hintAlphabet)

type hintPhase uint8

const (
	hintIdle hintPhase = iota
	hintPending
	hintShown
)

// hintTarget is a labelled click target. x and y are global logical
// coordinates of its centre.
type hintTarget struct {
	x, y   float64
	window WindowID
	label  string
	rect   ports.Rect
	text   bool // found by OCR
}

type hintState struct {
	phase  hintPhase
	seq    uint64 // last request sequence number
	output string
	// ws is the workspace on the hint output when hint mode began: another
	// one on screen ends it.
	ws      *Workspace
	geom    hintGeom
	windows []ports.HintWindow // visible windows at request time
	targets []hintTarget
	typed   string
}

// hintGeom is the output geometry a scan was requested on: a result or a
// label set is only good for the same one.
type hintGeom struct {
	x, y          int
	w, h          int // logical size
	width, height int // buffer size, physical pixels
	scale         float64
	transform     ports.BufferTransform
}

func (sc *screen) hintGeom() hintGeom {
	r := sc.rect()
	return hintGeom{x: sc.x, y: sc.y, w: r.W, h: r.H, width: sc.info.Width, height: sc.info.Height, scale: sc.scale, transform: sc.transform}
}

// hintRequest starts hint mode on the focused screen. It does nothing
// (reporting once through Options.HintDisabled) without a scanner, while the session is protected, while hint mode is
// active, or on an output with a non-normal transform.
func (c *Core) hintRequest() {
	if c.ch.HintScans == nil {
		if c.opts.HintDisabled != nil {
			c.opts.HintDisabled()
			c.opts.HintDisabled = nil
		}
		return
	}
	if c.security.Protected || c.hint.phase != hintIdle || c.hintPointerBusy() {
		return
	}
	sc := c.cur()
	if sc.name() == "" || sc.transform != 0 || sc.mon.ov.open || sc.info.Width <= 0 || sc.info.Height <= 0 {
		return
	}
	wins := c.hintWindows(sc)
	req := ports.HintScanRequest{Seq: c.hint.seq + 1, Output: sc.name(), Width: sc.info.Width, Height: sc.info.Height, Scale: sc.scale, LinksOnly: c.cfg.Hint.LinksOnly, Windows: wins}
	select {
	case c.ch.HintScans <- req:
	default:
		return
	}
	c.hint = hintState{phase: hintPending, seq: req.Seq, output: sc.name(), ws: sc.mon.Current(), geom: sc.hintGeom(), windows: slices.Clone(wins)}
}

// hintWindows lists the windows drawn on sc, bottom to top, with their client
// area (placements are output-local).
func (c *Core) hintWindows(sc *screen) []ports.HintWindow {
	view := sc.mon.viewport()
	var out []ports.HintWindow
	for _, p := range sc.shownLayout() {
		if p.Hidden || p.Preview > 0 || !view.shows(p) {
			continue
		}
		r := c.clientRect(p)
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		w := ports.HintWindow{ID: p.ID, Rect: r, Focused: p.Focused}
		rec := c.windows.lookup(p.ID)
		w.PID, w.AppID = rec.client.PID, rec.client.AppID
		out = append(out, w)
	}
	return out
}

// hintPointerBusy reports whether the pointer is in use by something a
// synthetic click would disturb: a pointer constraint, a grab, a held
// button (swallowed ones included: they stay in c.buttons), a window or data
// drag, or the column switcher.
func (c *Core) hintPointerBusy() bool {
	return c.constraint.Mode != ports.ConstraintNone || c.constrained.Mode != ports.ConstraintNone ||
		c.grab != 0 || len(c.buttons) > 0 || c.drag != nil || c.dataDrag != nil || c.switching()
}

// hintResult handles a scanner result and reports whether to publish.
func (c *Core) hintResult(res ports.HintScanResult) bool {
	h := &c.hint
	if h.phase != hintPending || res.Seq != h.seq || res.Output != h.output {
		return false
	}
	if res.Err != nil || c.security.Protected {
		c.hintCancel()
		return true
	}
	i := c.screenIndex(h.output)
	if i < 0 {
		c.hintCancel()
		return true
	}
	sc := c.screens[i]
	if sc.hintGeom() != h.geom {
		c.hintCancel()
		return true
	}
	out := sc.rect()
	var kept []hintTarget
	for _, t := range res.Targets {
		x := float64(sc.x) + float64(t.Rect.X) + float64(t.Rect.W)/2
		y := float64(sc.y) + float64(t.Rect.Y) + float64(t.Rect.H)/2
		if x < float64(out.X) || y < float64(out.Y) || x >= float64(out.X+out.W) || y >= float64(out.Y+out.H) {
			continue
		}
		id, _, _ := c.hit(x, y)
		if id == 0 {
			continue
		}
		root := c.popupRoot(id)
		if s, _ := c.screenOf(root); s != sc {
			// A layer surface, or a window of another output.
			continue
		}
		if !c.hintOwned(sc, t, root) {
			continue
		}
		kept = append(kept, hintTarget{x: x, y: y, window: id, rect: t.Rect, text: t.Text})
	}
	if len(kept) == 0 {
		c.hintCancel()
		return true
	}
	slices.SortStableFunc(kept, func(a, b hintTarget) int {
		return cmp.Or(cmp.Compare(a.rect.Y, b.rect.Y), cmp.Compare(a.rect.X, b.rect.X))
	})
	if len(kept) > hintMaxTargets {
		kept = kept[:hintMaxTargets]
	}
	n := len(hintAlphabet)
	for i := range kept {
		if len(kept) <= n {
			kept[i].label = string(hintAlphabet[i])
		} else {
			kept[i].label = string([]byte{hintAlphabet[i/n], hintAlphabet[i%n]})
		}
	}
	h.phase, h.targets, h.typed = hintShown, kept, ""
	return true
}

// hintOwned reports whether the window under a target is the one it was
// scanned on. An accessibility target names its window; an OCR target only
// counts over a window that was visible when the scan was requested and has
// not moved or resized since.
func (c *Core) hintOwned(sc *screen, t ports.HintTarget, root WindowID) bool {
	if t.Window != 0 {
		return root == t.Window
	}
	for _, w := range c.hint.windows {
		if w.ID != root {
			continue
		}
		for _, p := range sc.shownLayout() {
			if p.ID == root {
				return c.clientRect(p) == w.Rect
			}
		}
	}
	return false
}

// hintCancel ends hint mode.
func (c *Core) hintCancel() { c.hint = hintState{seq: c.hint.seq} }

// hintValidate ends hint mode when what it was started on is gone: the
// session became protected, the output went or changed geometry, another workspace is on screen
// there, or the overview opened. It runs at the start of every publish.
func (c *Core) hintValidate() {
	h := &c.hint
	if h.phase == hintIdle {
		return
	}
	i := c.screenIndex(h.output)
	if c.security.Protected || i < 0 || c.screens[i].mon.ov.open || c.screens[i].mon.Current() != h.ws || c.cur().mon.ov.open {
		c.hintCancel()
		return
	}
	if c.screens[i].hintGeom() != h.geom {
		c.hintCancel()
	}
}

// hintKey handles a key while hint mode is active: the key never goes on.
// A press is the whole job; releases take the usual swallow path.
func (c *Core) hintKey(ctx context.Context, key ports.KeyEvent) (bool, error) {
	if c.hint.phase == hintIdle || !key.Pressed {
		return false, nil
	}
	c.pressed[heldKey(key)] = true
	h := &c.hint
	if h.phase == hintPending || keyName(key.Keysym) == "Escape" {
		c.hintCancel()
		return true, c.publish(ctx)
	}
	if keyName(key.Keysym) == "BackSpace" {
		if h.typed != "" {
			h.typed = h.typed[:len(h.typed)-1]
		}
		return true, c.publish(ctx)
	}
	name := key.Base
	if name == "" {
		name = key.Keysym
	}
	name = keyName(name)
	if len(name) != 1 || name[0] < 'a' || name[0] > 'z' {
		c.hintCancel()
		return true, c.publish(ctx)
	}
	typed := h.typed + name
	var match *hintTarget
	any := false
	for i := range h.targets {
		if len(h.targets[i].label) >= len(typed) && h.targets[i].label[:len(typed)] == typed {
			any = true
			if h.targets[i].label == typed {
				match = &h.targets[i]
			}
		}
	}
	if !any {
		return true, nil
	}
	if match == nil {
		h.typed = typed
		return true, c.publish(ctx)
	}
	return true, c.hintClick(ctx, *match)
}

// hintClick ends hint mode and left-clicks the target's centre, provided the
// pointer is free and the same window is still under it. The click is
// synthetic: c.synthetic keeps the physical modifiers (the bind's Cmd may
// still be down) from turning it into a window drag, without touching c.mods.
//
// A target found by OCR is clicked with hint.text-click held (terminals open
// a link on a modified click). Every click gives the window the keyboard
// first, then sends exactly the modifiers to hold (none for other targets),
// and the real modifier state is always sent back afterwards.
func (c *Core) hintClick(ctx context.Context, t hintTarget) (err error) {
	c.hintCancel()
	if err := c.publish(ctx); err != nil {
		return err
	}
	if !c.hintClickable(t) {
		return nil
	}
	// Every click states its modifiers (possibly none): the bind's Cmd may
	// still be down and the client must not see it on a plain click.
	if err := c.clickFocus(ctx, t.window); err != nil {
		return err
	}
	if err := c.command(ctx, ports.SetModifiers{ID: t.window, Mods: c.hintMods(t)}); err != nil {
		return err
	}
	defer func() {
		if e := c.command(ctx, ports.SetModifiers{ID: t.window, Restore: true, State: c.keyState}); err == nil {
			err = e
		}
	}()
	c.synthetic = true
	defer func() { c.synthetic = false }()
	if err := c.pointerMotion(ctx, ports.PointerMotion{X: t.x, Y: t.y, Time: c.motionTime}); err != nil {
		return err
	}
	// The motion may have been bent (constraint) or the layout changed:
	// press only where the target is.
	if c.cursorX != t.x || c.cursorY != t.y || !c.hintClickable(t) {
		return nil
	}
	if err := c.pointerButton(ctx, ports.PointerButton{Button: btnLeft, Pressed: true, Time: c.motionTime}); err != nil {
		return err
	}
	return c.pointerButton(ctx, ports.PointerButton{Button: btnLeft, Time: c.motionTime})
}

// hintMods are the modifiers to hold for a click on t, read at click time so
// a reloaded configuration applies.
func (c *Core) hintMods(t hintTarget) ports.Mods {
	if !t.text {
		return 0
	}
	return c.cfg.Hint.TextClick
}

// hintClickable reports whether a click on t may go out now: unprotected,
// pointer free, and the selected window is still under the target's centre.
func (c *Core) hintClickable(t hintTarget) bool {
	if c.security.Protected || c.hintPointerBusy() {
		return false
	}
	id, _, _ := c.hit(t.x, t.y)
	return id == t.window
}

// hintLabels are the labels to draw on sc: those still matching what was
// typed. Nil unless sc is the hint output.
func (c *Core) hintLabels(sc *screen) []ports.HintLabel {
	h := &c.hint
	if h.phase != hintShown || sc.name() != h.output {
		return nil
	}
	var out []ports.HintLabel
	for _, t := range h.targets {
		if len(t.label) < len(h.typed) || t.label[:len(h.typed)] != h.typed {
			continue
		}
		out = append(out, ports.HintLabel{Rect: ports.Rect{X: t.rect.X, Y: t.rect.Y}, Text: t.label, Typed: len(h.typed)})
	}
	return out
}
