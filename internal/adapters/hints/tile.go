package hints

const (
	// maxRegions is the protocol limit of regions per request.
	maxRegions = 64
	// wholeMaxSide is the longest frame side sent whole when the frame is not
	// 16:9: up to there the square letterbox of the detector loses little.
	wholeMaxSide = 2560
	// tileMaxH is the tallest tile; its width is 16/9 of it.
	tileMaxH = 1440
	// tileCutMargin is the distance, in physical pixels, to a tile's inner
	// edge under which a box counts as cut by it.
	tileCutMargin = 2
)

// region is a crop of the frame, in physical pixels.
type region struct{ x, y, w, h int }

// tileRegions returns the crops to detect separately on a width x height
// frame, or nil for one whole-frame detection (a 16:9 frame, or one whose
// long side is at most wholeMaxSide). Otherwise the frame is covered by exact
// 16:9 tiles (w*9 == h*16; explicit crops always use square letterboxing) of
// height min(height, 1440) rounded down to a multiple of 9, overlapping by at
// least 10 % of the tile side on each axis; the last tile of a row or column
// is aligned to the frame edge. Tiles are listed row by row. A frame that
// would need more than maxRegions tiles is detected whole.
func tileRegions(width, height int) []region {
	if width <= 0 || height <= 0 || width*9 == height*16 || max(width, height) <= wholeMaxSide {
		return nil
	}
	th := min(height, tileMaxH) / 9 * 9
	tw := th / 9 * 16
	if tw > width { // a tall frame: the width bounds the tile
		tw = width / 16 * 16
		th = tw / 16 * 9
	}
	if tw == 0 || th == 0 {
		return nil
	}
	xs, ys := tileStarts(width, tw), tileStarts(height, th)
	if len(xs)*len(ys) > maxRegions {
		return nil
	}
	out := make([]region, 0, len(xs)*len(ys))
	for _, y := range ys {
		for _, x := range xs {
			out = append(out, region{x, y, tw, th})
		}
	}
	return out
}

// tileStarts returns the start offsets of tiles of size t covering length l,
// evenly spread, the last one flush with the end, with consecutive tiles
// overlapping by at least a tenth of t.
func tileStarts(l, t int) []int {
	if l <= t {
		return []int{0}
	}
	d := l - t
	maxStep := t * 9 / 10
	n := (d+maxStep-1)/maxStep + 1
	out := make([]int, n)
	for i := range out {
		out[i] = (d*i + n - 2) / (n - 1) // ceil(d*i/(n-1)): steps never exceed maxStep
	}
	return out
}

// dropCutBoxes drops the boxes that a tile's inner edge cut (an edge of the
// tile that is not an edge of the frame) when another tile saw the same
// element uncut: the cut box would otherwise survive as a smaller box nested
// in the whole one. A box is "the same element" when the other box covers
// nestRatio of it. Without regions the boxes are returned as they are.
func dropCutBoxes(dets []detection, regions []region) []detection {
	if len(regions) == 0 {
		return dets
	}
	fw, fh := 0, 0
	for _, r := range regions {
		fw, fh = max(fw, r.x+r.w), max(fh, r.y+r.h)
	}
	cut := func(d detection) bool {
		if d.region < 0 || d.region >= len(regions) {
			return false
		}
		r := regions[d.region]
		m := float32(tileCutMargin)
		return (r.x > 0 && d.x1 < float32(r.x)+m) || (r.x+r.w < fw && d.x2 > float32(r.x+r.w)-m) ||
			(r.y > 0 && d.y1 < float32(r.y)+m) || (r.y+r.h < fh && d.y2 > float32(r.y+r.h)-m)
	}
	isCut := make([]bool, len(dets))
	for i, d := range dets {
		isCut[i] = cut(d)
	}
	out := make([]detection, 0, len(dets))
	for i, d := range dets {
		drop := false
		if isCut[i] {
			for j, o := range dets {
				if j != i && !isCut[j] && o.region != d.region && covered(d, o) {
					drop = true
					break
				}
			}
		}
		if !drop {
			out = append(out, d)
		}
	}
	return out
}

// covered reports whether o covers at least nestRatio of d's area.
func covered(d, o detection) bool {
	w := min(d.x2, o.x2) - max(d.x1, o.x1)
	h := min(d.y2, o.y2) - max(d.y1, o.y1)
	if w <= 0 || h <= 0 {
		return false
	}
	return float64(w*h) >= nestRatio*float64((d.x2-d.x1)*(d.y2-d.y1))
}
