package workspace

import (
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

// MarkOp is the operation of a mark action.
type MarkOp uint8

// Mark operations, one per mark action prefix.
const (
	MarkSet MarkOp = iota + 1
	MarkToggle
	MarkUnmark
	MarkFocus
	MarkSwap
	MarkMoveTo
)

// Mark action prefixes: "mark term", "focus-mark term foot".
const (
	markPrefix       = "mark "
	toggleMarkPrefix = "toggle-mark "
	unmarkPrefix     = "unmark "
	focusMarkPrefix  = "focus-mark "
	swapMarkPrefix   = "swap-mark "
	moveToMarkPrefix = "move-to-mark "
)

var markPrefixes = []struct {
	prefix string
	op     MarkOp
}{
	{markPrefix, MarkSet},
	{toggleMarkPrefix, MarkToggle},
	{unmarkPrefix, MarkUnmark},
	{focusMarkPrefix, MarkFocus},
	{swapMarkPrefix, MarkSwap},
	{moveToMarkPrefix, MarkMoveTo},
}

// MarkArg parses a mark action. A bare "unmark" gives MarkUnmark with name
// "". cmd is set only for focus-mark with a command after the name. ok is
// false on a missing or invalid name, or extra fields on other ops.
func MarkArg(a Action) (op MarkOp, name string, cmd []string, ok bool) {
	if a == Action(strings.TrimSpace(unmarkPrefix)) {
		return MarkUnmark, "", nil, true
	}
	for _, p := range markPrefixes {
		rest, found := strings.CutPrefix(string(a), p.prefix)
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 || !domain.ValidMarkName(fields[0]) {
			return 0, "", nil, false
		}
		if len(fields) > 1 {
			if p.op != MarkFocus {
				return 0, "", nil, false
			}
			cmd = fields[1:]
		}
		return p.op, fields[0], cmd, true
	}
	return 0, "", nil, false
}

// isDialogParent reports whether a float of w is a dialog over id.
func (w *Workspace) isDialogParent(id WindowID) bool {
	return slices.ContainsFunc(w.Floats, func(f Float) bool { return f.over == id })
}

// markable reports whether id may trade places with a marked window: not
// stashed, fullscreen or held, no dialog and no parent of one.
func (w *Workspace) markable(id WindowID) bool {
	if id == 0 || w.StashIndex(id) >= 0 || w.IsFullscreenMember(id) || w.IsHeld(id) || w.isDialogParent(id) {
		return false
	}
	if i := w.floatIndex(id); i >= 0 {
		return w.Floats[i].over == 0
	}
	return true
}

// SwapWindows exchanges two tiles, or two plain floats, of a and b (which
// may be the same workspace) in place: columns, widths, float geometry and
// focus indexes stay with the places. It reports false, changing nothing,
// for a tile with a float, the same window, or any window markable refuses.
func SwapWindows(a *Workspace, ida WindowID, b *Workspace, idb WindowID) bool {
	if ida == idb || !a.markable(ida) || !b.markable(idb) {
		return false
	}
	if ca, ra := a.Cell(ida); ca > 0 {
		cb, rb := b.Cell(idb)
		if cb == 0 {
			return false
		}
		a.Columns[ca-1].Windows[ra-1], b.Columns[cb-1].Windows[rb-1] = idb, ida
		swapKeyed(a, ida, b, idb)
		return true
	}
	fa, fb := a.floatIndex(ida), b.floatIndex(idb)
	if fa < 0 || fb < 0 {
		return false
	}
	a.Floats[fa].ID, b.Floats[fb].ID = idb, ida
	swapKeyed(a, ida, b, idb)
	return true
}

// swapKeyed moves the ID-keyed state of a swap with what it describes.
// Maximized and recent name a column by one of its windows: they describe
// the place, so each workspace renames the window that left for the one
// that came. left describes the window (its refused fullscreen requests):
// it goes with the window.
func swapKeyed(a *Workspace, ida WindowID, b *Workspace, idb WindowID) {
	rename := func(ids []WindowID) {
		for i, v := range ids {
			switch v {
			case ida:
				ids[i] = idb
			case idb:
				ids[i] = ida
			}
		}
	}
	rename(a.Maximized)
	rename(a.recent)
	if b != a {
		rename(b.Maximized)
		rename(b.recent)
	}
	if b == a {
		return
	}
	la, lb := a.left == ida, b.left == idb
	if la {
		a.left = 0
	}
	if lb {
		b.left = 0
	}
	if la && b.left == 0 {
		b.left = ida
	}
	if lb && a.left == 0 {
		a.left = idb
	}
}

// MoveTileBelow moves id, the focused plain tile of from, into the column
// of the plain tile target of to, right below it, and focuses it there.
// It reports false, changing nothing, otherwise.
func MoveTileBelow(from *Workspace, id WindowID, to *Workspace, target WindowID) bool {
	if id == target || !from.markable(id) || !to.markable(target) {
		return false
	}
	if focused, ok := from.Focused(); !ok || focused != id || from.OnFloat() {
		return false
	}
	if c, _ := from.Cell(id); c == 0 {
		return false
	}
	if c, _ := to.Cell(target); c == 0 {
		return false
	}
	Drop(from, to, id, DropSpot{Kind: DropBelow, Anchor: target})
	to.FocusID(id)
	return true
}
