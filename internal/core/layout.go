package core

import (
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"
)

type WindowID = ports.WindowID

type Rect = ports.Rect

type Width struct{ Num, Den, Pixels int }

func ParseWidth(s string) (Width, error) {
	if s == "1" {
		return Width{Num: 1, Den: 1}, nil
	}
	if strings.HasSuffix(s, "px") {
		n, e := strconv.Atoi(strings.TrimSuffix(s, "px"))
		if e == nil && n > 0 {
			return Width{Pixels: n}, nil
		}
	} else if strings.HasSuffix(s, "%") {
		n, e := strconv.Atoi(strings.TrimSuffix(s, "%"))
		if e == nil && n > 0 && n <= 100 {
			return Width{Num: n, Den: 100}, nil
		}
	} else {
		p := strings.Split(s, "/")
		if len(p) == 2 {
			a, e1 := strconv.Atoi(p[0])
			b, e2 := strconv.Atoi(p[1])
			if e1 == nil && e2 == nil && a > 0 && a <= b {
				return Width{Num: a, Den: b}, nil
			}
		}
	}
	return Width{}, fmt.Errorf("invalid width %q", s)
}

// Resolve returns column pixels: round(frac * (usableW - gaps)) - gaps.
// Widths are clamped to the available area (zero if no space remains).
func (v Width) Resolve(usableW, gaps int) int {
	if usableW <= 0 {
		return 0
	}
	gaps = min(max(gaps, 0), usableW/2)
	limit := max(usableW-2*gaps, 0)
	if limit == 0 {
		return 0
	}
	if v.Pixels > 0 {
		return min(v.Pixels, limit)
	}
	if v.Den <= 0 || v.Num <= 0 {
		return 1
	}
	// Cap invalid fractions above one before multiplying; the result would
	// be clamped to the available width anyway.
	num, den := uint64(min(v.Num, v.Den)), uint64(v.Den)
	avail := uint64(usableW - gaps)
	q, r := avail/den, avail%den
	hi, lo := bits.Mul64(r, num)
	lo, carry := bits.Add64(lo, den/2, 0)
	hi += carry
	// hi < den: r and num are both smaller than den.
	fraction, _ := bits.Div64(hi, lo, den)
	rounded := q*num + fraction // no larger than avail (plus rounding)
	if rounded <= uint64(gaps) {
		return 1
	}
	return int(min(rounded-uint64(gaps), uint64(limit)))
}

// Column.Width is zero (auto) until the user picks a preset: auto columns share
// the usable width equally, up to MaxColumns visible at once.
type Column struct {
	Windows   []WindowID
	Width     Width
	FullWidth bool
	// Expanded is the fixed-overflow wide column: max-columns - 1 cells,
	// the others share the last one. One column at most has it.
	Expanded bool
	Focus    int
	// Slot is the declared column number (workspace.<name>.column.N) of a
	// slot window; 0 for normal columns.
	Slot int
}
type Placement struct {
	ID                          WindowID
	Rect                        Rect
	Fullscreen, Focused, Hidden bool
	// Floating windows sit over the columns at their own size.
	Floating bool
	// Neighbors are the sides touching another tiled window. Inset are the
	// sides where the border takes room from the window: all sides of a
	// float; for tiles, the right and bottom neighbors only, so two
	// windows share one separator line (tmux style). With gaps, each tile
	// insets all its neighbor sides.
	Neighbors, Inset ports.Sides
}

// Overflow says what happens past MaxColumns columns.
type Overflow string

const (
	// OverflowScroll opens further columns to the right; the view scrolls.
	OverflowScroll Overflow = "scroll"
	// OverflowFixed keeps every column on screen: past the max, each new
	// column splits the newest one, alternating top/bottom and left/right
	// (dwindle spiral).
	OverflowFixed Overflow = "fixed"
)

type Workspace struct {
	// ID stays with the workspace across reorder and monitor moves.
	ID uint64
	// Name is set for workspaces declared in config; empty for dynamic ones.
	Name       string
	Overflow   Overflow
	Columns    []Column
	Focus      int
	ViewX      int
	Output     Rect
	Usable     Rect
	Gaps       int
	MaxColumns int
	// border is drawn around floating windows, inside their rect.
	border     int
	presets    []Width
	fullscreen WindowID
	// Floats are floating windows, bottom to top; floatFocus is set while
	// the top one has the focus instead of the columns.
	Floats       []Float
	floatFocus   bool
	floatsHidden bool
	// hiddenFullscreen holds a float's fullscreen while the floats are
	// hidden, restored when shown.
	hiddenFullscreen WindowID
	// home is the monitor (key or connector) the workspace belongs to; ""
	// means the one it is on. On another monitor it is a guest; homePos is
	// its position there, where it returns.
	home    string
	homePos int
	// termAt is when core last spawned a terminal for this workspace.
	termAt time.Time
	// origin is set on the workspace a fixed-overflow fullscreen window
	// moved to; back is where it returns there (Monitor.fullscreenHome).
	origin *Workspace
	back   origPlace
}

// origPlace remembers where a window was: its column, its row in a stacked
// column, the column's width and slot, or its floating size.
type origPlace struct {
	// id is the window that went fullscreen; tiled lists the windows that
	// joined its workspace tiled, floating there until they go home.
	id             WindowID
	tiled          []WindowID
	col, row, slot int
	// stacked holds the windows left in the column: the window returns to
	// its row in the column that still holds one of them.
	stacked  []WindowID
	width    Width
	expanded bool
	// fullWidth is the column's maximize-column state.
	fullWidth bool
	float     *Float
}

// Float is a floating window and its client size, logical.
type Float struct {
	ID   WindowID
	W, H int
	// back records the former column of a tiled window; nil for native floats.
	back *origPlace
}

func (w *Workspace) empty() bool { return len(w.Columns) == 0 && len(w.Floats) == 0 }

// windows lists every window: columns first, then floating ones.
func (w *Workspace) windows() []WindowID {
	var ids []WindowID
	for _, c := range w.Columns {
		ids = append(ids, c.Windows...)
	}
	for _, f := range w.Floats {
		ids = append(ids, f.ID)
	}
	return ids
}

func (w *Workspace) floatIndex(id WindowID) int {
	return slices.IndexFunc(w.Floats, func(f Float) bool { return f.ID == id })
}

// AddFloating shows a window over the columns and focuses it.
func (w *Workspace) AddFloating(id WindowID, width, height int) {
	if id == 0 || w.has(id) {
		return
	}
	if w.floatsHidden {
		// A new float must be seen: show the hidden ones with it.
		w.ToggleFloatingVisible()
	}
	w.Floats = append(w.Floats, Float{ID: id, W: width, H: height})
	w.floatFocus = true
}

// ResizeFloating records the size a floating window draws.
func (w *Workspace) ResizeFloating(id WindowID, width, height int) {
	if i := w.floatIndex(id); i >= 0 {
		if w.Floats[i].back == nil {
			w.Floats[i].W, w.Floats[i].H = width, height
		}
	}
}

// has reports whether the workspace holds the window.
func (w *Workspace) has(id WindowID) bool {
	if w.floatIndex(id) >= 0 {
		return true
	}
	for _, c := range w.Columns {
		for _, v := range c.Windows {
			if v == id {
				return true
			}
		}
	}
	return false
}

func (w *Workspace) Focused() (WindowID, bool) {
	if w.floatFocus && !w.floatsHidden && len(w.Floats) > 0 {
		return w.Floats[len(w.Floats)-1].ID, true
	}
	if w.Focus < 0 || w.Focus >= len(w.Columns) {
		return 0, false
	}
	c := w.Columns[w.Focus]
	if c.Focus < 0 || c.Focus >= len(c.Windows) {
		return 0, false
	}
	return c.Windows[c.Focus], true
}
func (w *Workspace) AddWindow(id WindowID) {
	if id == 0 || w.has(id) {
		return
	}
	w.addColumn(Column{Windows: []WindowID{id}})
}

// addColumn places a new column like a new window and focuses it.
func (w *Workspace) addColumn(col Column) {
	at := 0
	if w.Overflow == OverflowFixed {
		// The spiral depends only on window order: new windows go last.
		at = len(w.Columns)
	} else if len(w.Columns) > 0 {
		at = w.Focus + 1
	}
	if w.Overflow == OverflowFixed && len(w.Columns) > 0 {
		w.Columns[w.Focus].FullWidth = false
	}
	w.Columns = append(w.Columns, Column{})
	copy(w.Columns[at+1:], w.Columns[at:])
	w.Columns[at] = col
	w.Focus = at
	w.scroll()
}

// AddSlotWindow places a slot window in its own column, ordered by slot
// number before the other columns. Focus stays on the focused window: slot
// windows are automatic events (ADR 011 golden rule).
func (w *Workspace) AddSlotWindow(id WindowID, slot int, width Width) {
	if id == 0 || w.has(id) {
		return
	}
	// Right after the last slot column with a lower number, else first.
	at := 0
	for i, c := range w.Columns {
		if c.Slot > 0 && c.Slot < slot {
			at = i + 1
		}
	}
	w.Columns = slices.Insert(w.Columns, at, Column{Windows: []WindowID{id}, Width: width, Slot: slot})
	if len(w.Columns) > 1 && at <= w.Focus {
		w.Focus++
	}
	w.scroll()
}

// setSlotWidth updates the width of slot column n, if present.
func (w *Workspace) setSlotWidth(n int, width Width) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Width = width
		}
	}
	w.scroll()
}

// inSlot reports whether window id is in slot column n.
func (w *Workspace) inSlot(id WindowID, n int) bool {
	for _, c := range w.Columns {
		if c.Slot == n && slices.Contains(c.Windows, id) {
			return true
		}
	}
	return false
}

// unslot turns slot column n into a normal column.
func (w *Workspace) unslot(n int) {
	for i := range w.Columns {
		if w.Columns[i].Slot == n {
			w.Columns[i].Slot = 0
		}
	}
}

func (w *Workspace) RemoveWindow(id WindowID) {
	if i := w.floatIndex(id); i >= 0 {
		w.Floats = slices.Delete(w.Floats, i, i+1)
		if w.fullscreen == id {
			w.fullscreen = 0
		}
		if w.hiddenFullscreen == id {
			w.hiddenFullscreen = 0
		}
		if len(w.Floats) == 0 {
			w.floatFocus, w.floatsHidden = false, false
		}
		return
	}
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v != id {
				continue
			}
			c := &w.Columns[i]
			c.Windows = append(c.Windows[:j], c.Windows[j+1:]...)
			if w.fullscreen == id {
				w.fullscreen = 0
			}
			if len(c.Windows) == 0 {
				w.Columns = append(w.Columns[:i], w.Columns[i+1:]...)
				if len(w.Columns) == 0 {
					w.Focus = 0
					w.ViewX = 0
					return
				}
				if i < w.Focus || (i == w.Focus && i > 0) {
					w.Focus--
				}
			} else {
				if j < c.Focus {
					c.Focus--
				} else if j == c.Focus && c.Focus > 0 {
					c.Focus--
				}
			}
			w.scroll()
			return
		}
	}
}

// FocusID selects a window and scrolls its column into view; a floating
// window is raised.
func (w *Workspace) FocusID(id WindowID) bool {
	if i := w.floatIndex(id); i >= 0 {
		if w.floatsHidden {
			return false
		}
		f := w.Floats[i]
		w.Floats = append(slices.Delete(w.Floats, i, i+1), f)
		w.floatFocus = true
		return true
	}
	w.floatFocus = false
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v == id {
				if w.Overflow == OverflowFixed && w.Focus != i && w.Focus < len(w.Columns) {
					w.Columns[w.Focus].FullWidth = false
				}
				w.Focus = i
				w.Columns[i].Focus = j
				w.scroll()
				return true
			}
		}
	}
	return false
}
func (w *Workspace) FocusColumn(dir int) {
	if w.floatFocus {
		// The first move leaves the floating window for the columns.
		w.floatFocus = false
		return
	}
	if i := w.columnToward(dir); i >= 0 {
		if w.Overflow == OverflowFixed {
			w.Columns[w.Focus].FullWidth = false
		}
		w.Focus = i
		w.scroll()
	}
}

// columnToward returns the column focus-column-left/right (dir -1/1) goes
// to, or -1 at the edge. Scroll overflow follows the column order; fixed
// overflow stacks columns (spiral, expanded strips), so it follows the
// screen: the column on that side.
func (w *Workspace) columnToward(dir int) int {
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) {
		return -1
	}
	if w.onScreenFocus() {
		return w.screenNeighbor(dir, 0)
	}
	if i := w.Focus + dir; i >= 0 && i < len(w.Columns) {
		return i
	}
	return -1
}

// onScreenFocus reports whether directional focus follows the screen:
// fixed overflow with every column visible.
func (w *Workspace) onScreenFocus() bool {
	return w.Overflow == OverflowFixed && w.fullscreen == 0 && !w.Columns[w.Focus].FullWidth
}

// FocusWindow moves focus inside the column; false means it was already at the edge.
// Fixed overflow also stacks columns (spiral, expanded strips): past the
// column edge, focus goes to the column on screen above or below.
func (w *Workspace) FocusWindow(dir int) bool {
	if w.floatFocus {
		w.floatFocus = false
		return true
	}
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) {
		return false
	}
	c := &w.Columns[w.Focus]
	if c.Focus+dir >= 0 && c.Focus+dir < len(c.Windows) {
		c.Focus += dir
		w.scroll()
		return true
	}
	if !w.onScreenFocus() {
		return false
	}
	if i := w.screenNeighbor(0, dir); i >= 0 {
		w.Focus = i
		w.Columns[i].Focus = 0
		if dir < 0 {
			w.Columns[i].Focus = len(w.Columns[i].Windows) - 1
		}
		return true
	}
	return false
}

// screenNeighbor returns the column on screen next to the focused one, left
// or right (dx -1/1) or above or below (dy -1/1), or -1. The closest wins,
// then the one sharing the longest edge, then the nearest in column order.
func (w *Workspace) screenNeighbor(dx, dy int) int {
	rects := w.columnRects()
	cur := rects[w.Focus]
	best, bestDist, bestOverlap := -1, 0, 0
	for i, r := range rects {
		var overlap, dist int
		switch {
		case dx < 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), cur.X-(r.X+r.W)
		case dx > 0:
			overlap, dist = min(cur.Y+cur.H, r.Y+r.H)-max(cur.Y, r.Y), r.X-(cur.X+cur.W)
		case dy < 0:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), cur.Y-(r.Y+r.H)
		default:
			overlap, dist = min(cur.X+cur.W, r.X+r.W)-max(cur.X, r.X), r.Y-(cur.Y+cur.H)
		}
		if i == w.Focus || overlap <= 0 || dist < 0 {
			continue
		}
		closer := best >= 0 && (dist < bestDist || dist == bestDist && (overlap > bestOverlap ||
			overlap == bestOverlap && abs(i-w.Focus) < abs(best-w.Focus)))
		if best < 0 || closer {
			best, bestDist, bestOverlap = i, dist, overlap
		}
	}
	return best
}

func abs(n int) int { return max(n, -n) }
func (w *Workspace) MoveColumn(dir int) {
	if w.floatFocus {
		return
	}
	if (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		i := w.Focus
		w.Columns[i], w.Columns[i+dir] = w.Columns[i+dir], w.Columns[i]
		w.Focus += dir
		w.scroll()
	}
}

// takeColumn removes the focused column and returns it, as a normal column.
func (w *Workspace) takeColumn() (Column, bool) {
	if len(w.Columns) == 0 || w.floatFocus {
		return Column{}, false
	}
	col := w.Columns[w.Focus]
	col.Slot, col.Expanded = 0, false
	if slices.Contains(col.Windows, w.fullscreen) {
		w.fullscreen = 0
	}
	w.Columns = slices.Delete(w.Columns, w.Focus, w.Focus+1)
	w.Focus = min(w.Focus, max(len(w.Columns)-1, 0))
	if len(w.Columns) == 0 {
		w.ViewX = 0
	} else {
		w.scroll()
	}
	return col, true
}

// insertColumn adds a column at index at (clamped) and focuses it.
func (w *Workspace) insertColumn(at int, col Column) {
	at = min(max(at, 0), len(w.Columns))
	if w.Overflow == OverflowFixed && len(w.Columns) > 0 {
		w.Columns[w.Focus].FullWidth = false
	}
	w.Columns = slices.Insert(w.Columns, at, col)
	w.Focus = at
	w.scroll()
}

// CycleWidth steps the focused column through the presets. Fixed overflow
// ignores presets: it toggles the expanded column instead.
func (w *Workspace) CycleWidth() {
	if w.Overflow == OverflowFixed {
		w.toggleExpanded()
		return
	}
	if len(w.Columns) == 0 || len(w.presets) == 0 || w.floatFocus {
		return
	}
	// auto → presets in order → auto.
	c := &w.Columns[w.Focus]
	next := Width{}
	if c.Width == next {
		next = w.presets[0]
	}
	for i, v := range w.presets {
		if v == c.Width && i+1 < len(w.presets) {
			next = w.presets[i+1]
			break
		}
	}
	c.Width = next
	c.FullWidth = false
	w.scroll()
}

// toggleExpanded makes the focused column the expanded one, or shrinks it
// back when it already is. One column at most is expanded.
func (w *Workspace) toggleExpanded() {
	if len(w.Columns) < 2 || max(w.MaxColumns, 1) < 2 || w.floatFocus {
		return
	}
	on := !w.Columns[w.Focus].Expanded
	for i := range w.Columns {
		w.Columns[i].Expanded = false
	}
	w.Columns[w.Focus].Expanded = on
	w.Columns[w.Focus].FullWidth = false
}

// ToggleFullWidth expands the focused tiled column without changing its saved width.
func (w *Workspace) ToggleFullWidth() {
	if len(w.Columns) == 0 || w.floatFocus {
		return
	}
	c := &w.Columns[w.Focus]
	c.FullWidth = !c.FullWidth
	w.scroll()
}

func (w *Workspace) ToggleFullscreen() {
	id, ok := w.Focused()
	if !ok {
		return
	}
	if w.fullscreen == id {
		w.fullscreen = 0
	} else {
		w.fullscreen = id
	}
	w.scroll()
}

// Activate focuses a window and makes it visible: a tiled window leaves
// another window's fullscreen, which would hide it.
func (w *Workspace) Activate(id WindowID) {
	if w.fullscreen != 0 && w.fullscreen != id && w.floatIndex(id) < 0 {
		w.fullscreen = 0
	}
	w.FocusID(id)
}

// SetFullscreen applies a client request. It never moves focus: client
// requests are automatic events (ADR 011 golden rule).
func (w *Workspace) SetFullscreen(id WindowID, on bool) {
	if w.floatIndex(id) >= 0 {
		if w.floatsHidden {
			// Applied when the floats are shown again.
			if on {
				w.hiddenFullscreen = id
			} else if w.hiddenFullscreen == id {
				w.hiddenFullscreen = 0
			}
			return
		}
		if on {
			w.fullscreen = id
		} else if w.fullscreen == id {
			w.fullscreen = 0
		}
		return
	}
	for i := range w.Columns {
		for _, v := range w.Columns[i].Windows {
			if v == id {
				if on {
					w.fullscreen = id
				} else if w.fullscreen == id {
					w.fullscreen = 0
				}
				w.scroll()
				return
			}
		}
	}
}
func (w *Workspace) SetOutput(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	old := w.Output
	w.Output = Rect{W: width, H: height}
	if w.Usable == old {
		w.Usable = w.Output
	}
	w.SetUsable(w.Usable)
}

// SetUsable clamps negative dimensions to zero and clips the usable rectangle
// to the output. Effective gaps shrink to fit the available area.
func (w *Workspace) SetUsable(r Rect) {
	r.X = min(max(r.X, 0), w.Output.W)
	r.Y = min(max(r.Y, 0), w.Output.H)
	r.W = min(max(r.W, 0), w.Output.W-r.X)
	r.H = min(max(r.H, 0), w.Output.H-r.Y)
	w.Usable = r
	w.scroll()
}
func (w *Workspace) SetGaps(g int) {
	if g < 0 {
		g = 0
	}
	w.Gaps = g
	w.scroll()
}
func (w *Workspace) SetPresets(v []Width) { w.presets = append([]Width(nil), v...) }
func (w *Workspace) SetMaxColumns(n int) {
	w.MaxColumns = n
	w.scroll()
}
func (w *Workspace) gap() int { return min(w.Gaps, w.Usable.W/2, w.Usable.H/2) }
func (w *Workspace) fullscreenColumn(i int) bool {
	for _, id := range w.Columns[i].Windows {
		if id == w.fullscreen && id != 0 {
			return true
		}
	}
	return false
}
func (w *Workspace) columnWidth(i int) int {
	if w.Columns[i].FullWidth {
		return max(w.Usable.W-2*w.gap(), 0)
	}
	if w.fullscreenColumn(i) {
		return w.Output.W
	}
	g := w.gap()
	// Fixed overflow never scrolls, so presets would push columns off screen.
	if w.Columns[i].Width == (Width{}) || w.Overflow == OverflowFixed {
		if len(w.Columns) == 1 {
			return max(w.Usable.W-2*g, 0)
		}
		// Equal shares of the width left after gaps; a remainder under k pixels stays empty.
		k := min(len(w.Columns), max(w.MaxColumns, 1))
		return max((w.Usable.W-g*(k+1))/k, 0)
	}
	return w.Columns[i].Width.Resolve(w.Usable.W, g)
}
func (w *Workspace) columnX(i int) int {
	x := w.Usable.X + w.gap()
	for j := 0; j < i; j++ {
		x += w.columnWidth(j) + w.gap()
	}
	return x
}
func (w *Workspace) scroll() {
	if w.Overflow == OverflowFixed {
		w.ViewX = 0
		return
	}
	if len(w.Columns) == 0 {
		return
	}
	left := w.columnX(w.Focus) - w.ViewX
	width := w.columnWidth(w.Focus)
	if w.fullscreenColumn(w.Focus) {
		w.ViewX += left
		return
	}
	minX := w.Usable.X + w.gap()
	maxX := w.Usable.X + w.Usable.W - w.gap()
	if width > maxX-minX || left < minX {
		w.ViewX += left - minX
	} else if left+width > maxX {
		w.ViewX += left + width - maxX
	}
}

// columnRects returns each column's area on screen, before stacking windows.
func (w *Workspace) columnRects() []Rect {
	g := w.gap()
	y, h := w.Usable.Y+g, max(w.Usable.H-2*g, 0)
	rects := make([]Rect, len(w.Columns))
	for i := range w.Columns {
		rects[i] = Rect{X: w.columnX(i) - w.ViewX, Y: y, W: w.columnWidth(i), H: h}
	}
	if w.Overflow == OverflowFixed && len(w.Columns) > 0 && w.Columns[w.Focus].FullWidth {
		rects[w.Focus].X = w.Usable.X + g
		return rects
	}
	k := max(w.MaxColumns, 1)
	if e := slices.IndexFunc(w.Columns, func(c Column) bool { return c.Expanded }); w.Overflow == OverflowFixed && e >= 0 && k > 1 && len(w.Columns) > 1 {
		return w.expandedRects(e, k, y, h)
	}
	if w.Overflow != OverflowFixed || len(w.Columns) <= k {
		return rects
	}
	// The last slot holds the spiral; the columns before it keep their place.
	area := rects[k-1]
	for i := k - 1; i < len(w.Columns)-1; i++ {
		rects[i], area = split(area, g, (i-k+1)%2 == 0)
	}
	rects[len(rects)-1] = area
	return rects
}

// expandedRects places expanded column e over k-1 cells in its place. The
// columns before it stack in a strip on its left, those after it on its
// right; the two strips share the last cell.
func (w *Workspace) expandedRects(e, k, y, h int) []Rect {
	g := w.gap()
	cell := max((w.Usable.W-g*(k+1))/k, 0)
	wide := (k-1)*cell + (k-2)*g
	rest := max(w.Usable.W-2*g-wide-g, 0)
	before, after := w.Columns[:e], w.Columns[e+1:]
	leftW, rightW := rest, rest
	if len(before) > 0 && len(after) > 0 {
		leftW = max((rest-g)/2, 0)
		rightW = max(rest-g-leftW, 0)
	}
	rects := make([]Rect, 0, len(w.Columns))
	x := w.Usable.X + g
	if len(before) > 0 {
		rects = append(rects, stackRects(Rect{X: x, Y: y, W: leftW, H: h}, len(before), g)...)
		x += leftW + g
	}
	rects = append(rects, Rect{X: x, Y: y, W: wide, H: h})
	if len(after) > 0 {
		rects = append(rects, stackRects(Rect{X: x + wide + g, Y: y, W: rightW, H: h}, len(after), g)...)
	}
	// Gaps wider than a narrow output would push columns past its edge.
	right := w.Usable.X + w.Usable.W
	for i := range rects {
		rects[i].X = min(rects[i].X, right)
		rects[i].W = min(rects[i].W, right-rects[i].X)
	}
	return rects
}

// stackRects cuts r in n rows with a gap between them; the last row takes
// the rounding remainder. Rows past the bottom (gaps taller than r) are
// clamped to it, like windows stacked in a column.
func stackRects(r Rect, n, gap int) []Rect {
	rows := make([]Rect, n)
	avail := max(r.H-(n-1)*gap, 0)
	height := avail / n
	yy, bottom := r.Y, r.Y+r.H
	for i := range rows {
		hh := height
		if i == n-1 {
			hh = avail - height*(n-1)
		}
		yy = min(yy, bottom)
		hh = min(hh, bottom-yy)
		rows[i] = Rect{X: r.X, Y: yy, W: r.W, H: hh}
		yy += hh + gap
	}
	return rows
}

// split cuts r in two halves with a gap: top/bottom when vertical, else left/right.
func split(r Rect, gap int, vertical bool) (Rect, Rect) {
	if vertical {
		h := max((r.H-gap)/2, 0)
		return Rect{X: r.X, Y: r.Y, W: r.W, H: h}, Rect{X: r.X, Y: r.Y + h + gap, W: r.W, H: max(r.H-gap-h, 0)}
	}
	w := max((r.W-gap)/2, 0)
	return Rect{X: r.X, Y: r.Y, W: w, H: r.H}, Rect{X: r.X + w + gap, Y: r.Y, W: max(r.W-gap-w, 0), H: r.H}
}

func (w *Workspace) Layout() []Placement {
	var result []Placement
	gap := w.gap()
	cols := w.columnRects()
	focusedID, _ := w.Focused()
	floatFocused := w.floatIndex(focusedID) >= 0
	for i, c := range w.Columns {
		col := cols[i]
		n := len(c.Windows)
		if n == 0 {
			continue
		}
		fullColumn := w.fullscreenColumn(i)
		if fullColumn {
			col.W = w.Output.W
		}
		available := max(col.H-(n-1)*gap, 0)
		height := available / n
		y := col.Y
		bottom := col.Y + col.H
		for j, id := range c.Windows {
			h := height
			if j == n-1 {
				h = available - height*(n-1)
			}
			y = min(y, bottom)
			h = min(h, bottom-y)
			r := Rect{X: col.X, Y: y, W: col.W, H: h}
			full := w.fullscreen == id && id != 0
			// Fixed overflow cannot scroll to other columns while one fills
			// the view. Keep them in place, but out of the scene.
			maximized := w.Overflow == OverflowFixed && w.Columns[w.Focus].FullWidth && i != w.Focus
			hidden := ((fullColumn || w.Overflow == OverflowFixed && w.fullscreen != 0) && !full) || maximized
			if full {
				// Scroll mode aligns the view on the column; fixed never scrolls.
				r = Rect{X: col.X, Y: 0, W: w.Output.W, H: w.Output.H}
				if w.Overflow == OverflowFixed {
					r.X = 0
				}
			}
			if hidden {
				r = Rect{}
			}
			focused := !floatFocused && i == w.Focus && j == c.Focus
			result = append(result, Placement{ID: id, Rect: r, Fullscreen: full, Focused: focused, Hidden: hidden})
			y += h + gap
		}
	}
	setNeighbors(result, gap)
	// Floating windows go last: they are drawn and hit on top.
	for _, f := range w.Floats {
		p := Placement{ID: f.ID, Rect: w.floatRect(f), Floating: true, Focused: floatFocused && f.ID == focusedID, Inset: ports.SideAll, Hidden: w.floatsHidden}
		if p.Hidden {
			p.Rect = Rect{}
			result = append(result, p)
			continue
		}
		// Floats stay above a fullscreen window: a dialog opened from a
		// fullscreen app must be seen.
		if w.fullscreen == f.ID {
			p.Rect, p.Fullscreen = Rect{W: w.Output.W, H: w.Output.H}, true
		}
		result = append(result, p)
	}
	return result
}

// setNeighbors marks the sides where tiles touch across the gap, from
// geometry alone: client sizes do not change as the view scrolls.
func setNeighbors(tiles []Placement, gap int) {
	overlap := func(a0, a1, b0, b1 int) bool { return a0 < b1 && b0 < a1 }
	for i := range tiles {
		a := &tiles[i]
		if a.Hidden || a.Fullscreen || a.Rect.W <= 0 || a.Rect.H <= 0 {
			continue
		}
		for j := range tiles {
			b := tiles[j].Rect
			if i == j || tiles[j].Hidden || tiles[j].Fullscreen || b.W <= 0 || b.H <= 0 {
				continue
			}
			r := a.Rect
			switch {
			case r.X+r.W+gap == b.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H):
				a.Neighbors |= ports.SideRight
			case b.X+b.W+gap == r.X && overlap(r.Y, r.Y+r.H, b.Y, b.Y+b.H):
				a.Neighbors |= ports.SideLeft
			case r.Y+r.H+gap == b.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W):
				a.Neighbors |= ports.SideBottom
			case b.Y+b.H+gap == r.Y && overlap(r.X, r.X+r.W, b.X, b.X+b.W):
				a.Neighbors |= ports.SideTop
			}
		}
		a.Inset = a.Neighbors
		if gap == 0 {
			a.Inset &= ports.SideRight | ports.SideBottom
		}
	}
}

// floatRect centres a floating window in the usable area, its border
// around its client size, clamped to the area.
func (w *Workspace) floatRect(f Float) Rect {
	u := w.Usable
	fw, fh := f.W, f.H
	if f.back != nil {
		fw, fh = max(u.W*80/100-2*w.border, 0), max(u.H*80/100-2*w.border, 0)
	}
	if f.back == nil && (fw <= 0 || fh <= 0) {
		fw, fh = u.W/2, u.H/2
	}
	fw, fh = min(fw+2*w.border, u.W), min(fh+2*w.border, u.H)
	return Rect{X: u.X + (u.W-fw)/2, Y: u.Y + (u.H-fh)/2, W: fw, H: fh}
}

// imposedFloat reports whether core, rather than the client, owns the size.
func (w *Workspace) imposedFloat(id WindowID) bool {
	i := w.floatIndex(id)
	return i >= 0 && w.Floats[i].back != nil
}

// ToggleWindowFloating converts a tile to an 80% float, remembering its
// column and row. Native floats instead become new columns.
func (w *Workspace) ToggleWindowFloating() {
	id, ok := w.Focused()
	// A fullscreen window (a Wine game in scanout) keeps its place: it
	// leaves fullscreen first, then may float.
	if !ok || id == w.fullscreen {
		return
	}
	if i := w.floatIndex(id); i >= 0 {
		f := w.Floats[i]
		w.RemoveWindow(id)
		if f.back == nil {
			w.addColumn(Column{Windows: []WindowID{id}})
		} else {
			back := f.back
			at := min(back.col, len(w.Columns))
			if i := slices.IndexFunc(w.Columns, back.holdsStack); i >= 0 {
				at = i
				c := &w.Columns[at]
				row := 0
				for _, v := range back.stacked[:back.row] {
					if slices.Contains(c.Windows, v) {
						row++
					}
				}
				c.Windows = slices.Insert(c.Windows, row, id)
				c.Focus = row
				w.Focus = at
				w.scroll()
			} else {
				// Its column is gone: it comes back expanded, unless another
				// column was expanded meanwhile (as leaveFullscreen).
				expanded := back.expanded && !slices.ContainsFunc(w.Columns, func(c Column) bool { return c.Expanded })
				w.insertColumn(at, Column{Windows: []WindowID{id}, Width: back.width, Slot: back.slot, FullWidth: back.fullWidth, Expanded: expanded})
			}
		}
		w.floatFocus = false
		return
	}
	col := w.Focus
	c := w.Columns[col]
	row := c.Focus
	lone := len(c.Windows) == 1
	back := &origPlace{col: col, row: row, slot: c.Slot, width: c.Width, fullWidth: c.FullWidth && lone, expanded: c.Expanded && lone}
	for _, v := range c.Windows {
		if v != id {
			back.stacked = append(back.stacked, v)
		}
	}
	w.RemoveWindow(id)
	w.AddFloating(id, 0, 0)
	w.Floats[len(w.Floats)-1].back = back
}

// ToggleFloatingVisible hides every float without changing its placement
// when a float has the focus, and shows them again from any window. A
// fullscreen float (a game in scanout) is never hidden: hiding it drops
// scanout and VRR.
func (w *Workspace) ToggleFloatingVisible() {
	if len(w.Floats) == 0 {
		return
	}
	if !w.floatsHidden {
		if w.floatFocus && w.floatIndex(w.fullscreen) < 0 {
			w.floatsHidden, w.floatFocus, w.hiddenFullscreen = true, false, 0
		}
		return
	}
	w.floatsHidden = false
	w.floatFocus = true
	if w.fullscreen == 0 && w.floatIndex(w.hiddenFullscreen) >= 0 {
		w.fullscreen = w.hiddenFullscreen
	}
	w.hiddenFullscreen = 0
}
