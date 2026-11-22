package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// markHolder returns the mapped window holding name, 0 if none.
func (c *Core) markHolder(name string) WindowID {
	for id, rec := range c.windows.records {
		if rec.client.ID != 0 && rec.marks.Has(name) {
			return id
		}
	}
	return 0
}

// mappedMarks returns the marks of a mapped window, ok false otherwise.
func (c *Core) mappedMarks(id WindowID) (domain.MarkSet, bool) {
	rec := c.windows.records[id]
	if rec == nil || rec.client.ID == 0 {
		return "", false
	}
	return rec.marks, true
}

// setMark puts name on id, taking it from its previous holder. It reports
// whether a mark changed.
func (c *Core) setMark(id WindowID, name string) bool {
	marks, ok := c.mappedMarks(id)
	if !ok || !domain.ValidMarkName(name) || marks.Has(name) {
		return false
	}
	if holder := c.markHolder(name); holder != 0 {
		held, _ := c.mappedMarks(holder)
		c.windows.setMarks(holder, held.Without(name))
	}
	c.windows.setMarks(id, marks.With(name))
	return true
}

// toggleMark removes name from id if it holds it, else sets it.
func (c *Core) toggleMark(id WindowID, name string) bool {
	marks, ok := c.mappedMarks(id)
	if !ok {
		return false
	}
	if marks.Has(name) {
		c.windows.setMarks(id, marks.Without(name))
		return true
	}
	return c.setMark(id, name)
}

// unmarkName removes name from whichever window holds it.
func (c *Core) unmarkName(name string) bool {
	holder := c.markHolder(name)
	if holder == 0 {
		return false
	}
	marks, _ := c.mappedMarks(holder)
	c.windows.setMarks(holder, marks.Without(name))
	return true
}

// unmarkAll clears every mark of id.
func (c *Core) unmarkAll(id WindowID) bool {
	marks, ok := c.mappedMarks(id)
	if !ok || marks.Empty() {
		return false
	}
	c.windows.setMarks(id, "")
	return true
}

// runMark runs a mark action on the focused window of the focused screen's
// current workspace. A missing window or mark is a no-op, as is any mark
// action while that screen's overview is open.
func (c *Core) runMark(ctx context.Context, op MarkOp, name string, cmd []string) error {
	if c.cur().mon.ov.open {
		return nil
	}
	focused, _ := c.cur().mon.Current().Focused()
	var changed bool
	switch op {
	case MarkSet:
		changed = c.setMark(focused, name)
	case MarkToggle:
		changed = c.toggleMark(focused, name)
	case MarkUnmark:
		if name == "" {
			changed = c.unmarkAll(focused)
		} else {
			changed = c.unmarkName(name)
		}
	case MarkFocus:
		holder := c.markHolder(name)
		if holder != 0 {
			return c.layoutAction(ctx, func() Effect {
				c.raise(holder)
				return Effect{}
			})
		}
		// Run-or-raise: spawn cmd once while no window holds the mark.
		if cmd == nil || c.placement.markPending(name, c.now()) {
			return nil
		}
		return c.layoutAction(ctx, func() Effect {
			return Effect{Spawn: true, Commands: [][]string{cmd}, Mark: name}
		})
	case MarkSwap, MarkMoveTo:
		return c.markMove(ctx, op, focused, c.markHolder(name))
	}
	if changed {
		return c.publish(ctx)
	}
	return nil
}

// claimMark gives a newly mapped window the mark of its spawn claim. A
// marked-only claim is consumed here; a stash claim stays for place.
func (c *Core) claimMark(v ports.WindowMapped) {
	c.placement.expireWorkspaceClaims(c.now())
	target, ok := c.placement.pending[v.Slot]
	if !ok || target.mark == "" || target.marked {
		return
	}
	c.setMark(v.ID, target.mark)
	if target.kind == spawnMarked {
		delete(c.placement.pending, v.Slot)
		return
	}
	// The stash claim stays for place, which may not consume it (a float
	// map): no later window takes the mark again. It keeps markRetry.
	target.marked = true
	c.placement.pending[v.Slot] = target
}

// slotWindow reports whether id fills a configured slot.
func (c *Core) slotWindow(id WindowID) bool {
	for _, st := range c.slots {
		if st.window == id {
			return true
		}
	}
	return false
}

// markMove swaps the focused window with the holder of a mark, or moves it
// below the holder, across workspaces and outputs. A refused move is a
// no-op.
func (c *Core) markMove(ctx context.Context, op MarkOp, id, holder WindowID) error {
	if id == 0 || holder == 0 || id == holder || c.slotWindow(id) || c.slotWindow(holder) {
		return nil
	}
	sa, wa := c.screenOf(id)
	sb, wb := c.screenOf(holder)
	if wa == nil || wb == nil {
		return nil
	}
	return c.layoutAction(ctx, func() Effect {
		if op == MarkSwap {
			if !SwapWindows(wa, id, wb, holder) {
				return Effect{}
			}
		} else if !MoveTileBelow(wa, id, wb, holder) {
			return Effect{}
		}
		sa.mon.normalize()
		sb.mon.normalize()
		if op == MarkMoveTo {
			c.raise(id)
		}
		return Effect{}
	})
}

// targetAllowed reports whether a do request may run a on a marked window.
func targetAllowed(a Action) bool {
	switch a {
	case ActionCloseWindow, ActionToggleFloating, ActionToggleFullscreen, ActionToggleWindowStash:
		return true
	}
	_, op, ok := WorkspaceArg(a)
	return ok && op == MoveWindowToWorkspace
}

// runTargeted runs an allowed action on id in its own workspace, without
// moving the keyboard focus or any current workspace: it borrows the
// workspace's selection for the action (Select, no scroll or unmaximize).
// It does nothing while the target's overview is open, or when id cannot
// take that selection (another window covers it fullscreen, or it is in a
// hidden stash). close-window closes the target wherever it is.
func (c *Core) runTargeted(ctx context.Context, id WindowID, a Action) error {
	defer c.traceAction(a)()
	s, w := c.screenOf(id)
	if s == nil || s.mon.ov.open {
		return nil
	}
	if a == ActionCloseWindow {
		return c.applyLayout(ctx, false, func() Effect { return Effect{Close: id} })
	}
	return c.applyLayout(ctx, false, func() Effect {
		prev, _ := w.Focused()
		restore := func(w *Workspace, prev WindowID) {
			if got, _ := w.Focused(); prev != 0 && prev != got && w.Has(prev) {
				w.Select(prev)
			}
		}
		w.Select(id)
		if got, _ := w.Focused(); got != id {
			restore(w, prev)
			return Effect{}
		}
		if _, _, ok := WorkspaceArg(a); ok {
			// The window moves alone and quietly (no selection, view or
			// maximize change on either side), never fullscreen, behind
			// no cover.
			if i, _, ok := s.mon.moveDest(a); ok && !w.IsFullscreenMember(id) {
				if dst := s.mon.Workspaces[i]; dst != w && dst.Fullscreen == 0 {
					MoveByRule(w, dst, id)
				}
			}
		} else {
			w.Apply(a)
		}
		restore(w, prev)
		s.mon.normalize()
		return Effect{}
	})
}
