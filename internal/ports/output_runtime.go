package ports

import "github.com/bnema/neferwl/internal/domain"

// OutputRuntimeRequest transfers a desired-set change to the backend owner.
// Reply must have capacity one; completion follows reconciliation.
type OutputRuntimeRequest struct {
	Name   string
	Mode   domain.OutputMode
	Remove bool
	Wake   chan<- struct{}
	Reply  chan<- OutputRuntimeResult
}

func (OutputRuntimeRequest) outputRuntimeRequest() {}

// OutputApplyIDRuntime marks the apply IDs the backend gives runtime output
// requests, so they never collide with core's own apply IDs.
const OutputApplyIDRuntime uint64 = 1 << 63

// OutputRuntimeResult reports the selected name and a control error code.
type OutputRuntimeResult struct{ Name, Err string }

func (OutputRuntimeResult) outputRuntimeResult() {}

var _ interface{ outputRuntimeRequest() } = OutputRuntimeRequest{}
var _ interface{ outputRuntimeResult() } = OutputRuntimeResult{}
