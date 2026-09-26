package syncfile

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// A pipe stands in for a sync file: readable means signalled.
func TestWaitReturnsWhenReadable(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte{1})
		w.Close()
	}()
	if err := Wait(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestWaitStopsOnCancel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := Wait(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

// POLLERR alone (a write end whose reader is gone) is not a signal.
func TestWaitReportsErrorEvents(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r.Close()
	if err := Wait(context.Background(), w); err == nil {
		t.Fatal("POLLERR taken as signalled")
	}
}
