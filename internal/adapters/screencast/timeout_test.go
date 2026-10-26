package screencast

import (
	"context"
	"testing"
	"time"
)

// boundIO interrupts a real blocking operation. Check runs on its caller,
// never on the timer goroutine, and reports even an otherwise ignored error.
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

// receive bounds channel observations made by the test goroutine.
//
//testpolicy:realtime service integration uses a private D-Bus daemon and Unix descriptors
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test result")
	}
	var zero T
	return zero
}
