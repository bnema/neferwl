package ports

// HintScanRequest asks the hint scanner to find clickable targets in the last
// frame of Output. Core sends it and expects one HintScanResult echoing Seq.
type HintScanRequest struct {
	Seq    uint64 // core-chosen, echoed back
	Output string
	// Width and Height are the output buffer size in physical pixels
	// (OutputInfo.Width/Height).
	Width, Height int
	Scale         float64 // screen scale
}

// HintTarget is a clickable region found in a frame.
type HintTarget struct {
	Rect  Rect    // logical, output-local
	Score float32 // detector confidence, 0 to 1
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
