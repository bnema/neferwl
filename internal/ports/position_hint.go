package ports

const (
	PositionHintMaxColumns = 16
	PositionHintMaxRows    = 16
	PositionHintDiameter   = 8
	// PositionHintActiveDiameter fits in the padding and gap around its cell.
	PositionHintActiveDiameter = 12
	PositionHintGap            = 10
	PositionHintPadding        = 10
	PositionHintBottom         = 48
	PositionHintRadius         = 10
)

// PositionHint is a bounded, comparable snapshot. Scenes copy it by value;
// neither fading nor a changed grid shares mutable storage with a renderer.
type PositionHint struct {
	Opacity              float32
	Vertical             bool
	Columns              uint8
	Counts               [PositionHintMaxColumns]uint8
	ActiveCol, ActiveRow uint8
}

func (h PositionHint) Shown() bool { return h.Opacity > 0 && h.Columns > 0 }

// Bounds is the hint's logical output-local background rectangle.
func (h PositionHint) Bounds(width, height int) Rect {
	if !h.Shown() {
		return Rect{}
	}
	cols, rows := min(int(h.Columns), PositionHintMaxColumns), 1
	for _, n := range h.Counts[:cols] {
		rows = max(rows, min(int(n), PositionHintMaxRows))
	}
	w := 2*PositionHintPadding + cols*PositionHintDiameter + (cols-1)*PositionHintGap
	ht := 2*PositionHintPadding + rows*PositionHintDiameter + (rows-1)*PositionHintGap
	return Rect{X: (width - w) / 2, Y: height - PositionHintBottom - ht, W: w, H: ht}
}
