package hints

import (
	"image"
	"math"
	"sort"

	"github.com/bnema/neferwl/internal/ports"
)

// underlineCandidate is a word with a thin line under it.
type underlineCandidate struct {
	word     int
	row      int // the line's row
	from, to int // x extent of the continuous line through the word
}

// markUnderlined sets Underlined, UnderL and UnderR on the words of a band
// that have a thin line under them. words are in the coordinates of g.
// Lines that run on far beyond the word (rules, table borders, input boxes)
// do not count, unless the extension is made of underlined words too.
func markUnderlined(src *image.Gray, words []OCRWord) {
	// Words are relative to the band's own origin, whatever the image is a
	// view of.
	b := src.Bounds()
	g := &image.Gray{Pix: src.Pix[src.PixOffset(b.Min.X, b.Min.Y):], Stride: src.Stride, Rect: image.Rect(0, 0, b.Dx(), b.Dy())}
	var cands []underlineCandidate
	for i, w := range words {
		// A single glyph, or a box narrower than tall, has a flat bottom
		// stroke (8, L, 2) that is not an underline.
		if len([]rune(w.Text)) < 2 || w.Rect.Dx() < w.Rect.Dy() {
			continue
		}
		if c, ok := underlineOf(g, w.Rect); ok {
			c.word = i
			cands = append(cands, c)
		}
	}
	for _, c := range cands {
		w := words[c.word]
		h := w.Rect.Dy()
		if (w.Rect.Min.X-c.from > h && !underlineCovered(words, cands, c, -1)) ||
			(c.to-w.Rect.Max.X > h && !underlineCovered(words, cands, c, 1)) {
			continue
		}
		words[c.word].Underlined, words[c.word].UnderL, words[c.word].UnderR = true, c.from, c.to
	}
}

// covered reports whether the line of c, on the given side of its word, is
// explained by underlined words on the same line up to within a glyph of its
// end.
func underlineCovered(words []OCRWord, cands []underlineCandidate, c underlineCandidate, dir int) bool {
	w := words[c.word].Rect
	h := w.Dy()
	cur := w.Min.X
	if dir > 0 {
		cur = w.Max.X
	}
	for {
		if (dir < 0 && cur-c.from <= h) || (dir > 0 && c.to-cur <= h) {
			return true
		}
		next := -1
		for _, o := range cands {
			r := words[o.word].Rect
			if abs(r.Max.Y-w.Max.Y) > max(3, h/3) {
				continue
			}
			var gap, reach int
			if dir < 0 {
				gap, reach = cur-r.Max.X, r.Min.X
			} else {
				gap, reach = r.Min.X-cur, r.Max.X
			}
			if gap < -2 || gap > h {
				continue
			}
			if next < 0 || (dir < 0 && reach < next) || (dir > 0 && reach > next) {
				next = reach
			}
		}
		if next < 0 || (dir < 0 && next >= cur) || (dir > 0 && next <= cur) {
			return false
		}
		cur = next
	}
}

// underlineOf looks for an underline under the word box r in g.
func underlineOf(g *image.Gray, r image.Rectangle) (underlineCandidate, bool) {
	b := g.Bounds()
	box := r.Intersect(b)
	h := r.Dy()
	if box.Dx() < 4 || h < 4 {
		return underlineCandidate{}, false
	}
	bg, ink := contrastOf(g, box)
	thr := min(40, max(20, ink/2))
	if ink < thr+10 {
		return underlineCandidate{}, false
	}
	top := max(b.Min.Y, r.Max.Y-(h*15+99)/100)
	bottom := min(b.Max.Y, r.Max.Y+(h*35+99)/100+1)
	maxThick := max(2, h/6)
	rowOK := func(y int) bool {
		row := g.Pix[(y-g.Rect.Min.Y)*g.Stride:]
		n := 0
		for x := box.Min.X; x < box.Max.X; x++ {
			if abs(int(row[x-g.Rect.Min.X])-bg) >= thr {
				n++
			}
		}
		return n*100 >= box.Dx()*85
	}
	thick, first := 0, 0
	for y := top; y <= bottom; y++ {
		if y < bottom && rowOK(y) {
			if thick == 0 {
				first = y
			}
			thick++
			continue
		}
		if thick > 0 {
			if thick > maxThick {
				return underlineCandidate{}, false // a block, not a line
			}
			row := first + thick/2
			from, to := box.Min.X, box.Max.X
			line := g.Pix[(row-g.Rect.Min.Y)*g.Stride:]
			on := func(x int) bool { return abs(int(line[x-g.Rect.Min.X])-bg) >= thr }
			for from > b.Min.X && on(from-1) {
				from--
			}
			for to < b.Max.X && on(to) {
				to++
			}
			return underlineCandidate{row: row, from: from, to: to}, true
		}
	}
	return underlineCandidate{}, false
}

// contrastOf returns the background level of box (the most common level,
// found in bins of 16 so antialiasing does not split it, then refined to the
// mean of that bin) and the contrast of its ink (the deviation reached by
// the strongest 3% of pixels).
func contrastOf(g *image.Gray, box image.Rectangle) (bg, ink int) {
	var hist [256]int
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for _, v := range g.Pix[(y-g.Rect.Min.Y)*g.Stride+box.Min.X-g.Rect.Min.X:][:box.Dx()] {
			hist[v]++
		}
	}
	total := box.Dx() * box.Dy()
	var bins [16]int
	for v, c := range hist {
		bins[v>>4] += c
	}
	best := 0
	for i, c := range bins {
		if c > bins[best] {
			best = i
		}
	}
	sum, n := 0, 0
	for v := best << 4; v < (best+1)<<4; v++ {
		sum += v * hist[v]
		n += hist[v]
	}
	bg = sum / max(n, 1)
	var dev [256]int
	for v, c := range hist {
		dev[abs(v-bg)] += c
	}
	n = 0
	for d := 255; d >= 0; d-- {
		n += dev[d]
		if n*100 >= total*3 {
			return bg, d
		}
	}
	return bg, 0
}

// underlinedLinks returns the targets for underlined words, joining the
// words of one continuous underline. A group that overlaps a URL target (in
// pixels) is left to it. Coordinates are scaled to logical.
func underlinedLinks(words []OCRWord, urls []image.Rectangle, scale float64) []ports.HintTarget {
	var us []OCRWord
	for _, w := range words {
		if w.Underlined {
			us = append(us, w)
		}
	}
	sort.Slice(us, func(i, j int) bool { return us[i].Rect.Min.X < us[j].Rect.Min.X })
	used := make([]bool, len(us))
	var out []ports.HintTarget
	for i := range us {
		if used[i] {
			continue
		}
		used[i] = true
		rect, last := us[i].Rect, us[i]
		chars, width := len(last.Text), last.Rect.Dx()
		for {
			next := -1
			for j := range us {
				c := us[j]
				if used[j] || c.Rect.Min.X < rect.Max.X-2 || abs(c.Rect.Max.Y-last.Rect.Max.Y) > max(3, rect.Dy()/3) {
					continue
				}
				avg := float64(width+c.Rect.Dx()) / float64(max(1, chars+len(c.Text)))
				gap := c.Rect.Min.X - rect.Max.X
				if float64(gap) > 1.2*avg || c.Rect.Min.X > last.UnderR+2 {
					continue
				}
				if next < 0 || c.Rect.Min.X < us[next].Rect.Min.X {
					next = j
				}
			}
			if next < 0 {
				break
			}
			used[next] = true
			last = us[next]
			rect = rect.Union(last.Rect)
			chars += len(last.Text)
			width += last.Rect.Dx()
		}
		dup := false
		for _, u := range urls {
			if overlap(rect, u) > .5 {
				dup = true
			}
		}
		if dup {
			continue
		}
		x, y := int(math.Floor(float64(rect.Min.X)/scale)), int(math.Floor(float64(rect.Min.Y)/scale))
		right, bottom := int(math.Ceil(float64(rect.Max.X)/scale)), int(math.Ceil(float64(rect.Max.Y)/scale))
		out = append(out, ports.HintTarget{Kind: ports.HintLink, Source: ports.HintSourceOCR, Score: 1, Rect: ports.Rect{X: x, Y: y, W: right - x, H: bottom - y}})
	}
	return out
}
