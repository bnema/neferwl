package core

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

type WindowID uint64

type Rect struct{ X, Y, W, H int }

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

type Column struct {
	Windows []WindowID
	Width   Width
	Focus   int
}
type Placement struct {
	ID                          WindowID
	Rect                        Rect
	Fullscreen, Focused, Hidden bool
}
type Workspace struct {
	Columns      []Column
	Focus        int
	ViewX        int
	Output       Rect
	Usable       Rect
	Gaps         int
	DefaultWidth Width
	presets      []Width
	fullscreen   WindowID
}

func (w *Workspace) Focused() (WindowID, bool) {
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
	if id == 0 {
		return
	}
	for _, c := range w.Columns {
		for _, v := range c.Windows {
			if v == id {
				return
			}
		}
	}
	at := 0
	if len(w.Columns) > 0 {
		at = w.Focus + 1
	}
	w.Columns = append(w.Columns, Column{})
	copy(w.Columns[at+1:], w.Columns[at:])
	w.Columns[at] = Column{Windows: []WindowID{id}, Width: w.DefaultWidth}
	w.Focus = at
	w.scroll()
}
func (w *Workspace) RemoveWindow(id WindowID) {
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
func (w *Workspace) FocusColumn(dir int) {
	if len(w.Columns) > 0 && (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		w.Focus += dir
		w.scroll()
	}
}
func (w *Workspace) FocusWindow(dir int) {
	if len(w.Columns) == 0 || (dir != -1 && dir != 1) {
		return
	}
	c := &w.Columns[w.Focus]
	if c.Focus+dir >= 0 && c.Focus+dir < len(c.Windows) {
		c.Focus += dir
		w.scroll()
	}
}
func (w *Workspace) MoveColumn(dir int) {
	if (dir == -1 || dir == 1) && w.Focus+dir >= 0 && w.Focus+dir < len(w.Columns) {
		i := w.Focus
		w.Columns[i], w.Columns[i+dir] = w.Columns[i+dir], w.Columns[i]
		w.Focus += dir
		w.scroll()
	}
}
func (w *Workspace) CycleWidth() {
	if len(w.Columns) == 0 || len(w.presets) == 0 {
		return
	}
	c := &w.Columns[w.Focus]
	next := 0
	for i, v := range w.presets {
		if v == c.Width {
			next = (i + 1) % len(w.presets)
			break
		}
	}
	c.Width = w.presets[next]
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
func (w *Workspace) SetDefaultWidth(v Width) { w.DefaultWidth = v }
func (w *Workspace) SetPresets(v []Width)    { w.presets = append([]Width(nil), v...) }
func (w *Workspace) gap() int                { return min(w.Gaps, w.Usable.W/2, w.Usable.H/2) }
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
	return w.Columns[i].Width.Resolve(w.Usable.W, w.gap())
}
func (w *Workspace) columnX(i int) int {
	x := w.Usable.X + w.gap()
	for j := 0; j < i; j++ {
		x += w.columnWidth(j) + w.gap()
	}
	return x
}
func (w *Workspace) scroll() {
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
func (w *Workspace) Layout() []Placement {
	var result []Placement
	gap := w.gap()
	for i, c := range w.Columns {
		x := w.columnX(i) - w.ViewX
		width := w.columnWidth(i)
		n := len(c.Windows)
		if n == 0 {
			continue
		}
		fullColumn := w.fullscreenColumn(i)
		available := max(w.Usable.H-(n+1)*gap, 0)
		height := available / n
		y := w.Usable.Y + gap
		for j, id := range c.Windows {
			h := height
			if j == n-1 {
				h = available - height*(n-1)
			}
			if y > w.Usable.Y+w.Usable.H {
				y = w.Usable.Y + w.Usable.H
			}
			h = min(h, w.Usable.Y+w.Usable.H-y)
			r := Rect{X: x, Y: y, W: width, H: h}
			full := w.fullscreen == id && id != 0
			hidden := fullColumn && !full
			if full {
				r = Rect{X: x, Y: 0, W: w.Output.W, H: w.Output.H}
			}
			if hidden {
				r = Rect{}
			}
			result = append(result, Placement{ID: id, Rect: r, Fullscreen: full, Focused: i == w.Focus && j == c.Focus, Hidden: hidden})
			y += h + gap
		}
	}
	return result
}
