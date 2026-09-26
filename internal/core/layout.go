package core

import (
	"fmt"
	"github.com/bnema/nefertty/internal/ports"
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
	Windows []WindowID
	Width   Width
	Focus   int
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
	Floats     []Float
	floatFocus bool
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
	stacked []WindowID
	width   Width
	float   *Float
}

// Float is a floating window and its client size, logical.
type Float struct {
	ID   WindowID
	W, H int
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
	w.Floats = append(w.Floats, Float{ID: id, W: width, H: height})
	w.floatFocus = true
}

// ResizeFloating records the size a floating window draws.
func (w *Workspace) ResizeFloating(id WindowID, width, height int) {
	if i := w.floatIndex(id); i >= 0 {
		w.Floats[i].W, w.Floats[i].H = width, height
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
	if w.floatFocus && len(w.Floats) > 0 {
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
		if len(w.Floats) == 0 {
			w.floatFocus = false
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
		f := w.Floats[i]
		w.Floats = append(slices.Delete(w.Floats, i, i+1), f)
		w.floatFocus = true
		return true
	}
	w.floatFocus = false
	for i := range w.Columns {
		for j, v := range w.Columns[i].Windows {
			if v == id {
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
	if len(w.Columns) > 0 && (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		w.Focus += dir
		w.scroll()
	}
}

// FocusWindow moves focus inside the column; false means it was already at the edge.
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
	return false
}
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
	col.Slot = 0
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
	w.Columns = slices.Insert(w.Columns, at, col)
	w.Focus = at
	w.scroll()
}

func (w *Workspace) CycleWidth() {
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
	k := max(w.MaxColumns, 1)
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
			// Fixed overflow keeps every column on screen: hide them all
			// behind a fullscreen window.
			hidden := (fullColumn || w.Overflow == OverflowFixed && w.fullscreen != 0) && !full
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
		p := Placement{ID: f.ID, Rect: w.floatRect(f), Floating: true, Focused: floatFocused && f.ID == focusedID, Inset: ports.SideAll}
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
	if fw <= 0 || fh <= 0 {
		fw, fh = u.W/2, u.H/2
	}
	fw, fh = min(fw+2*w.border, u.W), min(fh+2*w.border, u.H)
	return Rect{X: u.X + (u.W-fw)/2, Y: u.Y + (u.H-fh)/2, W: fw, H: fh}
}
