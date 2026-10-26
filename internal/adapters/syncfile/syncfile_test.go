package syncfile

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// A pipe stands in for a sync file: readable means signalled.
//
//testpolicy:realtime real pipe poll in syncfile.Wait, bounded by a 1s guard
func TestWaitReturnsWhenReadable(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, r) }()
	if _, err := w.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A canceled ctx ends the wait on a pipe that never signals.
//
//testpolicy:realtime real pipe poll in syncfile.Wait
func TestWaitStopsOnCancel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, r); !errors.Is(err, context.Canceled) {
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
