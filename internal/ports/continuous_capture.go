package ports

// ContinuousCaptureRequest starts or stops owner-local frame writers.
// Reply must have capacity one. Empty Outputs selects all current outputs.
type ContinuousCaptureRequest struct {
	Outputs   []string
	Dir       string
	Raw, Stop bool
	Reply     chan<- ContinuousCaptureResult
}

func (ContinuousCaptureRequest) continuousCaptureRequest() {}

// ContinuousCaptureResult acknowledges owner-side writer changes.
type ContinuousCaptureResult struct{ Dir, Err string }

func (ContinuousCaptureResult) continuousCaptureResult() {}

var _ interface{ continuousCaptureRequest() } = ContinuousCaptureRequest{}
var _ interface{ continuousCaptureResult() } = ContinuousCaptureResult{}
