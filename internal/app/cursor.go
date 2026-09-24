package app

import (
	"math"
	"os"
	"strconv"

	"github.com/bnema/nefertty/internal/adapters/xcursor"
	"github.com/bnema/nefertty/internal/ports"
)

// cursorSize is XCURSOR_SIZE (logical pixels), 24 by default.
func cursorSize() int {
	if n, err := strconv.Atoi(os.Getenv("XCURSOR_SIZE")); err == nil && n >= 8 && n <= 256 {
		return n
	}
	return 24
}

// loadCursor loads the XCURSOR_THEME arrow at XCURSOR_SIZE × scale physical
// pixels, capped at limit pixels on a side.
func loadCursor(scale float64, limit int) (ports.CursorImage, error) {
	size := int(math.Round(float64(cursorSize()) * max(scale, 1)))
	img, err := xcursor.Load(xcursor.SearchPath(), os.Getenv("XCURSOR_THEME"), []string{"default", "left_ptr"}, size)
	if err != nil {
		return ports.CursorImage{}, err
	}
	img = img.ForSize(size, limit)
	return ports.CursorImage{W: img.W, H: img.H, HotX: img.HotX, HotY: img.HotY, Pixels: img.Pixels}, nil
}
