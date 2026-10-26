package geom

// BorderZone returns the origin and size of a padded zone between two edges.
func BorderZone(a, b, pad int) (int, int) {
	mid := (a + b) / 2
	lo, hi := min(a, mid-pad), max(b, mid+pad)
	return lo, max(hi-lo, 1)
}
