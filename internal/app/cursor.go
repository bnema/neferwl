package app

import (
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/ports"
)

// cursorSize is XCURSOR_SIZE (logical pixels), 24 by default.
func cursorSize() int {
	if n, err := strconv.Atoi(os.Getenv("XCURSOR_SIZE")); err == nil && n >= 8 && n <= 256 {
		return n
	}
	return 24
}

// legacyCursorNames are the X11 names of CSS cursors for themes without
// the CSS ones.
var legacyCursorNames = map[string][]string{
	"default":     {"left_ptr"},
	"pointer":     {"hand2", "hand1"},
	"text":        {"xterm"},
	"wait":        {"watch"},
	"progress":    {"left_ptr_watch"},
	"crosshair":   {"cross"},
	"move":        {"fleur"},
	"grab":        {"openhand"},
	"grabbing":    {"closedhand"},
	"not-allowed": {"crossed_circle"},
	"help":        {"question_arrow"},
	"ew-resize":   {"sb_h_double_arrow"},
	"ns-resize":   {"sb_v_double_arrow"},
	"col-resize":  {"sb_h_double_arrow"},
	"row-resize":  {"sb_v_double_arrow"},
	"e-resize":    {"right_side"},
	"w-resize":    {"left_side"},
	"n-resize":    {"top_side"},
	"s-resize":    {"bottom_side"},
	"ne-resize":   {"top_right_corner"},
	"nw-resize":   {"top_left_corner"},
	"se-resize":   {"bottom_right_corner"},
	"sw-resize":   {"bottom_left_corner"},
	"nesw-resize": {"fd_double_arrow"},
	"nwse-resize": {"bd_double_arrow"},
	"all-scroll":  {"fleur"},
	"alias":       {"link"},
	"cell":        {"plus"},
	"no-drop":     {"crossed_circle"},
}

// themeCursors caches loaded theme cursors: clients change shapes on every
// hover and each output loads them on its render goroutine.
var themeCursors = struct {
	sync.Mutex
	m map[themeKey]ports.CursorImage
}{m: map[themeKey]ports.CursorImage{}}

type themeKey struct {
	shape       string
	size, limit int
}

// loadCursor returns the cursor a client asked for at an output scale, at
// most limit physical pixels on a side: a XCURSOR_THEME shape at
// XCURSOR_SIZE × scale, the client image resized to the output scale, or
// an empty image when hidden. Unknown shapes fall back to the arrow.
func loadCursor(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error) {
	scale = max(scale, 1)
	if c.Hidden {
		return ports.CursorImage{}, nil
	}
	if c.Image != nil {
		img := xcursor.Image{W: c.Image.W, H: c.Image.H, HotX: c.Image.HotX, HotY: c.Image.HotY, Pixels: c.Image.Pixels}
		f := scale / float64(max(c.Scale, 1))
		w, h := int(math.Round(float64(img.W)*f)), int(math.Round(float64(img.H)*f))
		if big := max(w, h); big > limit {
			w, h = w*limit/big, h*limit/big
		}
		img = img.Scale(max(w, 1), max(h, 1))
		return ports.CursorImage{W: img.W, H: img.H, HotX: img.HotX, HotY: img.HotY, Pixels: img.Pixels}, nil
	}
	shape := c.Shape
	if shape == "" {
		shape = "default"
	}
	size := int(math.Round(float64(cursorSize()) * scale))
	key := themeKey{shape, size, limit}
	themeCursors.Lock()
	defer themeCursors.Unlock()
	if img, ok := themeCursors.m[key]; ok {
		return img, nil
	}
	// The shape is looked up through the whole theme chain before the
	// arrow: a theme may override only the arrow and inherit the rest.
	dirs, theme := xcursor.SearchPath(), os.Getenv("XCURSOR_THEME")
	img, err := xcursor.Load(dirs, theme, append([]string{shape}, legacyCursorNames[shape]...), size)
	if err != nil && shape != "default" {
		img, err = xcursor.Load(dirs, theme, []string{"default", "left_ptr"}, size)
	}
	if err != nil {
		return ports.CursorImage{}, err
	}
	img = img.ForSize(size, limit)
	out := ports.CursorImage{W: img.W, H: img.H, HotX: img.HotX, HotY: img.HotY, Pixels: img.Pixels}
	themeCursors.m[key] = out
	return out, nil
}
