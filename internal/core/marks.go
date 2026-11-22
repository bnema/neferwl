package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
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
		if holder == 0 {
			_ = cmd // run-or-raise spawning lands with spawn claims
			return nil
		}
		return c.layoutAction(ctx, func() Effect {
			c.raise(holder)
			return Effect{}
		})
	}
	if changed {
		return c.publish(ctx)
	}
	return nil
}
