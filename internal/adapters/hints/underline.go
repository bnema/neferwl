package hints

import (
	"image"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bnema/neferwl/internal/domain"
)

const (
	// underlineMinContrast and underlineMaxContrast clamp the ink/2 threshold used to tell line pixels from background.
	underlineMinContrast = 20
	underlineMaxContrast = 40
	// underlineSearchPct is the search depth below a word, in percent of its height.
	underlineSearchPct = 35
	// underlineRowCoveragePct is the share of the word width a row must cover to count as line.
	underlineRowCoveragePct = 85
	// underlineThicknessDiv gives the maximum line thickness as word height / this (at least 2 px).
	underlineThicknessDiv = 6
	// underlineBorderPct is the share of the word height a vertical stroke above a line end must reach to reject it as a border.
	underlineBorderPct = 75
)

// underlineCandidate is a word with a thin line under it.
type underlineCandidate struct {
	word     int
	from, to int // x extent of the continuous line through the word
}

// markUnderlined sets Underlined and UnderR on the words of a band
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
		if utf8.RuneCountInString(w.Text) < 2 || w.Rect.Dx() < w.Rect.Dy() {
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
		words[c.word].Underlined, words[c.word].UnderR = true, c.to
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
			if !sameBaseline(w, r, h) {
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
	thr := min(underlineMaxContrast, max(underlineMinContrast, ink/2))
	if ink < thr+10 {
		return underlineCandidate{}, false
	}
	top := max(b.Min.Y, r.Max.Y)
	bottom := min(b.Max.Y, r.Max.Y+(h*underlineSearchPct+99)/100+1)
	maxThick := max(2, h/underlineThicknessDiv)
	rowOK := func(y int) bool {
		row := g.Pix[(y-g.Rect.Min.Y)*g.Stride:]
		n := 0
		for x := box.Min.X; x < box.Max.X; x++ {
			if abs(int(row[x-g.Rect.Min.X])-bg) >= thr {
				n++
			}
		}
		return n*100 >= box.Dx()*underlineRowCoveragePct
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
			// A vertical stroke at either end identifies a box/table border.
			// Look above the line, away from its own thickness.
			for _, x := range []int{from, to - 1} {
				n := 0
				for y := first - 1; y >= max(b.Min.Y, first-h); y-- {
					if abs(int(g.GrayAt(x, y).Y)-bg) >= thr {
						n++
					} else {
						break
					}
				}
				if n*100 >= h*underlineBorderPct {
					return underlineCandidate{}, false
				}
			}
			return underlineCandidate{from: from, to: to}, true
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
// words of one continuous underline, excluding groups already covered by URL
// targets. Coordinates are scaled to logical.
func underlinedLinks(words []OCRWord, urls []image.Rectangle, scale float64) []domain.HintTarget {
	var us []OCRWord
	for _, w := range words {
		if w.Underlined {
			us = append(us, w)
		}
	}
	sort.Slice(us, func(i, j int) bool { return us[i].Rect.Min.X < us[j].Rect.Min.X })
	used := make([]bool, len(us))
	var out []domain.HintTarget
	for i := range us {
		if used[i] {
			continue
		}
		used[i] = true
		rect, last := us[i].Rect, us[i]
		chars, width := utf8.RuneCountInString(last.Text), last.Rect.Dx()
		text := last.Text
		for {
			next := -1
			for j := range us {
				c := us[j]
				if used[j] || c.Rect.Min.X < rect.Max.X-2 || !sameBaseline(last.Rect, c.Rect, rect.Dy()) {
					continue
				}
				avg := float64(width+c.Rect.Dx()) / float64(max(1, chars+utf8.RuneCountInString(c.Text)))
				gap := c.Rect.Min.X - rect.Max.X
				if float64(gap) > 1.2*avg || c.Rect.Min.X > last.UnderR+2 {
					continue
				}
				next = j // us is sorted by x: the first eligible word is the leftmost
				break
			}
			if next < 0 {
				break
			}
			used[next] = true
			if touching(rect, us[next].Rect) {
				text += us[next].Text
			} else {
				text += " " + us[next].Text
			}
			last = us[next]
			rect = rect.Union(last.Rect)
			chars += utf8.RuneCountInString(last.Text)
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
		url := ""
		if !strings.Contains(text, " ") {
			url, _ = linkURL(text)
		}
		out = append(out, domain.HintTarget{Kind: domain.HintLink, Source: domain.HintSourceOCR, Score: 1, URL: url, Rect: logicalRect(rect, scale)})
	}
	return out
}
