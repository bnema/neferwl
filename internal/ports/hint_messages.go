package ports

import "github.com/bnema/neferwl/internal/domain"

// HintWindow is a toplevel and its mapped popups, output-local and logical.
type HintWindow struct {
	ID      domain.WindowID
	PID     int
	AppID   string
	Rect    domain.Rect
	Popups  []domain.Rect
	Focused bool
}

func (w HintWindow) Region() []domain.Rect { return append([]domain.Rect{w.Rect}, w.Popups...) }
func (w HintWindow) Bounds() domain.Rect {
	var b domain.Rect
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
		b = domain.Rect{X: x, Y: y, W: max(b.X+b.W, r.X+r.W) - x, H: max(b.Y+b.H, r.Y+r.H) - y}
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
	Rect   domain.Rect // logical, output-local
	Score  float32     // detector confidence, 0 to 1; 1 for AT-SPI and OCR
	Kind   domain.HintKind
	Source domain.HintSource
	Window domain.WindowID // toplevel unit ID
}

// HintScanResult answers a HintScanRequest. Core owns Targets.
type HintScanResult struct {
	Seq     uint64
	Output  string
	Targets []HintTarget
	Err     error
}
