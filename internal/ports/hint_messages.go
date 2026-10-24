package ports

// HintSource identifies the source of a target; config determines chain order.
type HintSource uint8

const (
	HintSourceATSPI HintSource = iota + 1
	HintSourceOCR
	HintSourceDetector
)

func (s HintSource) String() string {
	switch s {
	case HintSourceATSPI:
		return "atspi"
	case HintSourceOCR:
		return "ocr"
	case HintSourceDetector:
		return "detector"
	}
	return ""
}

type HintKind uint8

const (
	HintLink HintKind = iota
	HintButton
)

// HintWindow is a toplevel and its mapped popups, output-local and logical.
type HintWindow struct {
	ID      WindowID
	PID     int
	AppID   string
	Rect    Rect
	Popups  []Rect
	Focused bool
}

func (w HintWindow) Region() []Rect { return append([]Rect{w.Rect}, w.Popups...) }
func (w HintWindow) Bounds() Rect {
	var b Rect
	for i := -1; i < len(w.Popups); i++ {
		r := w.Rect
		if i >= 0 {
			r = w.Popups[i]
		}
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		if b.W <= 0 || b.H <= 0 {
			b = r
			continue
		}
		x, y := min(b.X, r.X), min(b.Y, r.Y)
		b = Rect{X: x, Y: y, W: max(b.X+b.W, r.X+r.W) - x, H: max(b.Y+b.H, r.Y+r.H) - y}
	}
	return b
}
func (w HintWindow) Contains(x, y int) bool {
	for i := -1; i < len(w.Popups); i++ {
		r := w.Rect
		if i >= 0 {
			r = w.Popups[i]
		}
		if x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H {
			return true
		}
	}
	return false
}

// HintScanRequest asks the hint scanner to find clickable targets in the last
// frame of Output. Core sends it and expects one HintScanResult echoing Seq.
type HintScanRequest struct {
	Seq    uint64 // core-chosen, echoed back
	Output string
	// Width and Height are the output buffer size in physical pixels
	// (OutputInfo.Width/Height).
	Width, Height int
	Scale         float64 // screen scale
	LinksOnly     bool
	Windows       []HintWindow // owned by core per request; adapters must not retain
}

// HintTarget is a clickable region found in a frame.
type HintTarget struct {
	Rect   Rect    // logical, output-local
	Score  float32 // detector confidence, 0 to 1; 1 for AT-SPI and OCR
	Kind   HintKind
	Source HintSource
	Window WindowID // toplevel unit ID
}

// HintScanResult answers a HintScanRequest. Core owns Targets.
type HintScanResult struct {
	Seq     uint64
	Output  string
	Targets []HintTarget
	Err     error
}

// HintLabel is a label drawn over a hint target.
type HintLabel struct {
	// Rect is the label box in logical output-local coordinates; X and Y are
	// the target's top-left corner, which the renderer anchors the label
	// outside of. W and H may be 0: the renderer sizes the box from Text.
	Rect Rect
	// Text is the full label, lowercase a–z.
	Text string
	// Typed is the number of leading characters already typed (drawn dimmed).
	Typed int
}
