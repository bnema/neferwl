package overlaydraw

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHintLabelSize(t *testing.T) {
	var r Raster
	_, w1, h1 := r.Label("f", 0, 1)
	_, w2, h2 := r.Label("fj", 0, 1)
	require.Greater(t, w1, 0)
	require.Greater(t, w2, w1, "a longer label is wider")
	require.Equal(t, h1, h2)
	_, w3, h3 := r.Label("fj", 0, 2)
	// The 1 px border does not scale: compare the boxes without it.
	require.InDelta(t, 2*(w2-2), w3-2, 1)
	require.InDelta(t, 2*(h2-2), h3-2, 1)
}

func TestHintLabelCache(t *testing.T) {
	var r Raster
	a, w, h := r.Label("fj", 1, 1)
	b, _, _ := r.Label("fj", 1, 1)
	require.Len(t, a, w*h*4)
	require.Same(t, &a[0], &b[0], "same key returns the cached slice")
	c, _, _ := r.Label("fj", 0, 1)
	require.NotSame(t, &a[0], &c[0], "typed count is part of the key")
}

func TestHintLabelPremultiplied(t *testing.T) {
	var r Raster
	pix, w, h := r.Label("ab", 1, 1.5)
	require.Len(t, pix, w*h*4)
	for i := 0; i < len(pix); i += 4 {
		a := pix[i+3]
		require.GreaterOrEqual(t, a, pix[i])
		require.GreaterOrEqual(t, a, pix[i+1])
		require.GreaterOrEqual(t, a, pix[i+2])
	}
}

func TestHintLabelTypedIsDimmed(t *testing.T) {
	var r Raster
	plain, _, _ := r.Label("fj", 0, 1)
	dim, _, _ := r.Label("fj", 1, 1)
	require.NotEqual(t, plain, dim)
}

func TestHintLabelEmptyAndCacheBound(t *testing.T) {
	var r Raster
	pix, w, h := r.Label("", 0, 1)
	require.Nil(t, pix)
	require.Zero(t, w+h)
	for i := 0; i < maxEntries+10; i++ {
		r.Label("a", 0, 1+float64(i)/100000)
	}
	require.LessOrEqual(t, len(r.cache), maxEntries)
}

func TestLabelTruncatesToMaxWidth(t *testing.T) {
	var r Raster
	style := Style{FontPx: 13, TextOnly: true, PadX: 0.01, PadY: 0.01}
	_, full, _ := r.Label("a rather long window title", 0, 1, style)
	style.MaxWidth = full / 2
	_, cut, _ := r.Label("a rather long window title", 0, 1, style)
	require.LessOrEqual(t, cut, full/2+1, "the cut text fits (padding rounds to 0)")
	require.Greater(t, cut, 0)
	style.MaxWidth = full + 10
	_, same, _ := r.Label("a rather long window title", 0, 1, style)
	require.Equal(t, full, same, "fitting text is not cut")
}

// Widths that cut a title the same way share a cache entry; a fitting
// title ignores MaxWidth.
func TestLabelMaxWidthNormalised(t *testing.T) {
	var r Raster
	style := Style{FontPx: 13, TextOnly: true, PadX: 0.01, PadY: 0.01}
	const title = "a rather long window title"
	_, full, _ := r.Label(title, 0, 1, style)
	style.MaxWidth = full + 10
	fit, _, _ := r.Label(title, 0, 1, style)
	style.MaxWidth = full + 11
	fit2, _, _ := r.Label(title, 0, 1, style)
	require.Same(t, &fit[0], &fit2[0])
	style.MaxWidth = full / 2
	a, _, _ := r.Label(title, 0, 1, style)
	style.MaxWidth = full/2 + 1
	b, _, _ := r.Label(title, 0, 1, style)
	n := r.Len()
	if &a[0] != &b[0] {
		// full/2 sat on a glyph edge: one more px stays in the next one.
		style.MaxWidth = full/2 + 2
		c, _, _ := r.Label(title, 0, 1, style)
		require.Same(t, &b[0], &c[0])
		require.Equal(t, n, r.Len())
	}
}

// A window's title is one entry, replaced when it changes.
func TestTitlesOneEntryPerWindow(t *testing.T) {
	var titles Titles
	style := Style{FontPx: 13, TextOnly: true, PadX: 0.01, PadY: 0.01, MaxWidth: 200}
	var most int
	for i := range 500 {
		titles.Title(1, "build "+strconv.Itoa(i)+" running", style)
		titles.Title(2, "editor", style)
		most = max(most, titles.Bytes())
	}
	require.Equal(t, 2, titles.Len())
	a, _, _ := titles.Title(2, "editor", style)
	b, _, _ := titles.Title(2, "editor", style)
	require.Same(t, &a[0], &b[0])
	one, w, h := titles.Title(1, "build 0 running", style)
	require.LessOrEqual(t, most, 2*(len(one)+w*h*4))
	for i := range maxTitles + 10 {
		titles.Title(uint64(10+i), "x", style)
	}
	require.LessOrEqual(t, titles.Len(), maxTitles)
}
