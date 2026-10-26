package powerkey

import (
	"context"
	"testing"
	"time"
)

// boundIO interrupts a real blocking operation. Check runs on its caller,
// never on the timer goroutine, and reports even an otherwise ignored error.
//
//testpolicy:realtime private D-Bus logind peer on Unix sockets, interrupted by a 5s context deadline
func boundIO(t *testing.T, operation string, closeIO func()) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stop := context.AfterFunc(ctx, closeIO)
	return func() {
		t.Helper()
		stop()
		expired := ctx.Err() != nil
		cancel()
		if expired {
			t.Fatalf("%s timed out after 5s (connection/process closed)", operation)
		}
	}
}
