package core

import (
	"errors"

	"github.com/bnema/neferwl/internal/ports"
)

// RuntimeOutputCode is the control error code for a runtime output add or
// removal that RuntimeAdd/RuntimeRemove (or its apply) refused: forbidden
// for an output control may not touch, bad-request otherwise. nil is success.
func RuntimeOutputCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRuntimeOutputForbidden):
		return ports.ControlErrForbidden
	default:
		return ports.ControlErrBadRequest
	}
}
