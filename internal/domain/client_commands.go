package domain

// GestureKind is a touchpad gesture a client can receive
// (zwp_pointer_gestures_v1).
type GestureKind uint8

const (
	GestureSwipe GestureKind = iota
	GesturePinch
	GestureHold
)

// OutputPlacement is one output in the global layout. X, Y, Width and
// Height are logical (after Transform: they swap for 90/270);
// physical = logical × Scale. Info.Width and Info.Height are the physical
// mode, the untransformed target.
type OutputPlacement struct {
	Info                OutputInfo
	X, Y, Width, Height int
	Scale               float64
	// Transform is how the target holds the scene (wl_output.transform).
	Transform BufferTransform
	// Primary is output.<name>.primary: the pointer starts on it.
	Primary bool
}

// Contains reports whether the logical point is on the output.
func (o OutputPlacement) Contains(x, y float64) bool {
	return x >= float64(o.X) && x < float64(o.X+o.Width) && y >= float64(o.Y) && y < float64(o.Y+o.Height)
}

// ToTarget maps a global logical point on the output to target pixels.
func (o OutputPlacement) ToTarget(x, y float64) (float64, float64) {
	s := o.Scale
	if s <= 0 {
		s = 1
	}
	px, py := (x-float64(o.X))*s, (y-float64(o.Y))*s
	sw, sh := o.Transform.Size(o.Info.Width, o.Info.Height)
	return o.Transform.ToBuffer(px, py, float64(sw), float64(sh))
}

// Layout is the global arrangement of outputs in logical pixels.
type Layout []OutputPlacement

// At returns the output under the logical point.
func (l Layout) At(x, y float64) (OutputPlacement, bool) {
	for _, o := range l {
		if o.Contains(x, y) {
			return o, true
		}
	}
	return OutputPlacement{}, false
}

// Clamp keeps the pointer on an output: a point outside every output stays
// on the output under (fromX, fromY), or the first one, clamped to its edges.
func (l Layout) Clamp(fromX, fromY, x, y float64) (float64, float64) {
	if len(l) == 0 {
		return x, y
	}
	if _, ok := l.At(x, y); ok {
		return x, y
	}
	o, ok := l.At(fromX, fromY)
	if !ok {
		o = l[0]
	}
	x = min(max(x, float64(o.X)), float64(o.X+o.Width)-1)
	y = min(max(y, float64(o.Y)), float64(o.Y+o.Height)-1)
	return x, y
}

// CursorImage is a cursor frame in premultiplied B8G8R8A8, W*4 bytes per
// row; (HotX, HotY) is the click point. Sizes are physical pixels.
type CursorImage struct {
	W, H, HotX, HotY int
	Pixels           []byte
}

// CursorChange carries wayland → outputs the cursor the focused client asks
// for: a CSS cursor name (wp_cursor_shape_v1), its own image, or none.
// The zero value is the default arrow.
type CursorChange struct {
	// Shape is a CSS cursor name such as "pointer" or "text".
	Shape string
	// Image is a client cursor at Scale buffer pixels per logical pixel.
	Image  *CursorImage
	Scale  int
	Hidden bool
}

// SpawnRequest carries core → launcher process arguments.
type SpawnRequest struct {
	// Security retains the admitting owner epoch through launcher backpressure.
	// A wired launcher rejects stale or protected requests before execution.
	Security SecurityState
	Argv     []string
	// Env adds KEY=value entries to the child environment.
	Env []string
}
