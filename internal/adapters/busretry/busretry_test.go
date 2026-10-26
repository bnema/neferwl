package busretry

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// A failing or returning attempt runs again until ctx ends, and Run returns
// without waiting out the delay.
func TestRunRetriesUntilCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := make(chan int, 8)
		n := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, time.Millisecond, "test feature", logging.For(ctx, "test"), func(context.Context) error {
				n++
				select {
				case calls <- n:
				case <-ctx.Done():
				}
				if n%2 == 0 {
					return nil
				}
				return errors.New("bus gone")
			})
		}()
		for want := 1; want <= 3; want++ {
			synctest.Wait()
			select {
			case got := <-calls:
				if got != want {
					t.Fatalf("attempt %d, want %d", got, want)
				}
			default:
				t.Fatalf("attempt %d never ran", want)
			}
			if want < 3 {
				time.Sleep(time.Duration(1<<(want-1)) * time.Millisecond)
			}
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run outlived its context")
		}
	})
}

// A long delay does not hold Run once ctx ends.
func TestRunStopsDuringDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
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
		// Ensure Run has reached the retry delay before cancelling it.
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run waits out the retry delay after cancel")
		}
	})
}
