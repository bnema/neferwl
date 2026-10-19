package hintlabel

import (
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
