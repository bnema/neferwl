package sched

import (
	"context"
	"runtime"
	"testing"

	"github.com/bnema/neferwl/internal/logging"
)

func TestRealtime(t *testing.T) {
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		result <- Realtime(logging.For(context.Background(), "sched"))
	}()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
