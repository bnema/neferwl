package hdrcursor

import (
	"bytes"
	"testing"
)

func TestCacheKeyGeometry(t *testing.T) {
	px := []byte{10, 20, 30, 255, 40, 50, 60, 255, 70, 80, 90, 255, 100, 110, 120, 255}
	for _, tc := range []struct {
		name       string
		w, h, side int
	}{{"dimensions", 1, 4, 4}, {"max-side", 2, 2, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			var c Cache
			c.Convert(px, 2, 2, 4, 203)
			before := c.Key()
			got := bytes.Clone(c.Convert(px, tc.w, tc.h, tc.side, 203))
			if c.Key() == before {
				t.Fatal("geometry missing from cache key")
			}
			var fresh Cache
			want := fresh.Convert(px, tc.w, tc.h, tc.side, 203)
			if !bytes.Equal(got, want) {
				t.Fatalf("stale conversion: %v want %v", got, want)
			}
		})
	}
}
