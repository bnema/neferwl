package busretry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// A failing or returning attempt runs again until ctx ends, and Run returns
// without waiting out the delay.
func TestRunRetriesUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan int, 8)
	n := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, time.Millisecond, "test feature", logging.For(ctx, "test"), func(context.Context) error {
			n++
			calls <- n
			if n%2 == 0 {
				return nil
			}
			return errors.New("bus gone")
		})
	}()
	for want := 1; want <= 3; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("attempt %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d never ran", want)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run outlived its context")
	}
}

// A long delay does not hold Run once ctx ends.
func TestRunStopsDuringDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, time.Hour, "test feature", logging.For(ctx, "test"), func(context.Context) error {
			close(started)
			return errors.New("fail")
		})
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run waits out the retry delay after cancel")
	}
}
