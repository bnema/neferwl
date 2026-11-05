package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestRuntimeOutputCode(t *testing.T) {
	for err, want := range map[error]string{
		nil:                       "",
		ErrRuntimeOutputForbidden: ports.ControlErrForbidden,
		fmt.Errorf("wrapped: %w", ErrRuntimeOutputForbidden): ports.ControlErrForbidden,
		errors.New("invalid mode"):                           ports.ControlErrBadRequest,
	} {
		if got := RuntimeOutputCode(err); got != want {
			t.Errorf("%v: %q want %q", err, got, want)
		}
	}
}
