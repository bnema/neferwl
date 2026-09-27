package app

import (
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCaptureUnknownOutputClosesFile(t *testing.T) {
	replies := make(chan ports.CaptureDone, 1)
	set := newOutputSet(replies)
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	set.routeCapture(ports.CaptureRequest{ID: 9, Output: "missing", Dst: ports.SHMBuffer{File: f}})
	select {
	case result := <-replies:
		if result.ID != 9 || result.Err == nil {
			t.Fatal(result)
		}
		if _, err := f.Stat(); err == nil {
			t.Fatal("request descriptor still open")
		}
	case <-time.After(time.Second):
		t.Fatal("no reply")
	}
}
