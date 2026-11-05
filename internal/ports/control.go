package ports

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// ControlRequest carries control adapter → core requests.
// Reply must have capacity ≥1; core never blocks on it.
type ControlRequest interface{ controlRequest() }

// ControlDo runs a catalog action and observes its completion.
type ControlDo struct {
	Action  string
	Wait    domain.WaitLevel
	Until   *domain.Until
	Timeout time.Duration
	Shots   []domain.ShotRequest
	Caps    domain.Caps
	Reply   chan<- ControlReply
}

func (ControlDo) controlRequest() {}

// ControlInject sends events through the core input path and observes completion.
type ControlInject struct {
	Shots   []domain.ShotRequest
	Events  []InputEvent
	Wait    domain.WaitLevel
	Until   *domain.Until
	Timeout time.Duration
	Caps    domain.Caps
	Reply   chan<- ControlReply
}

func (ControlInject) controlRequest() {}

// ControlQuery reads the requested state.
type ControlQuery struct {
	What  string
	Caps  domain.Caps
	Reply chan<- ControlReply
}

func (ControlQuery) controlRequest() {}

// ControlReply carries completion and partial results, including on timeout.
// Shots contains output-specific presented scene sequences, not pixel data.
// Core performs no capture I/O: the adapter issues CaptureRequests using each
// returned MinSeq and reads their pixels. On timeout only presented outputs
// have Shots.
type ControlReply struct {
	// Err is empty on success; ControlErrTimeout reports a wait that ended
	// before its condition, with the partial results below.
	Err   string
	Done  domain.WaitLevel
	State domain.State
	Diff  domain.StateDiff
	Late  []domain.WindowID
	Shots []domain.ShotRequest
}

func (ControlReply) controlReply() {}

// ControlReply implements its seal so the method counts as used.
var _ interface{ controlReply() } = ControlReply{}

// Control error codes describe rejected requests.
const (
	ControlErrUnknownCommand = "unknown-command"
	ControlErrBadRequest     = "bad-request"
	ControlErrLocked         = "locked"
	// ControlErrBusy: too many control requests are already waiting.
	ControlErrBusy          = "busy"
	ControlErrForbidden     = "forbidden"
	ControlErrCaptureFailed = "capture-failed"
	// ControlErrTimeout: the completion condition was not reached in time;
	// the reply still carries the partial results.
	ControlErrTimeout = "timeout"
)

// ControlOutput changes the runtime-only virtual output desired set.
type ControlOutput struct {
	Name   string
	Mode   domain.OutputMode
	Remove bool
	Caps   domain.Caps
	Reply  chan<- ControlReply
}

func (ControlOutput) controlRequest() {}
