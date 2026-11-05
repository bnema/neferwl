package ports

// ContinuousTarget is one output's frame writer change: Dir is where its
// frames go (unused when stopping).
type ContinuousTarget struct{ Output, Dir string }

func (ContinuousTarget) continuousTarget() {}

// ContinuousCaptureRequest is core's resolved plan for owner-local frame
// writers: every target is a live output and every Dir is absolute. Reply
// must have capacity one; the backend acknowledges the whole plan once.
type ContinuousCaptureRequest struct {
	// Dir is the requested directory, echoed in the reply.
	Dir       string
	Targets   []ContinuousTarget
	Raw, Stop bool
	Reply     chan<- ContinuousCaptureResult
}

func (ContinuousCaptureRequest) continuousCaptureRequest() {}

// ContinuousCaptureResult acknowledges writer changes: Dir is the requested
// directory, Err a control error code (empty on success).
type ContinuousCaptureResult struct{ Dir, Err string }

func (ContinuousCaptureResult) continuousCaptureResult() {}

// OutputContinuous is one output owner's writer change (backend → output).
type OutputContinuous struct {
	Dir       string
	Raw, Stop bool
	Reply     chan<- ContinuousCaptureResult
}

func (OutputContinuous) outputContinuous() {}

var _ interface{ continuousCaptureRequest() } = ContinuousCaptureRequest{}
var _ interface{ continuousCaptureResult() } = ContinuousCaptureResult{}
var _ interface{ outputContinuous() } = OutputContinuous{}
var _ interface{ continuousTarget() } = ContinuousTarget{}
