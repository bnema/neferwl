package ports

import (
	"errors"
	"os"
	"testing"
)

// A dropped lease message closes the fd it owns; others own none.
func TestCloseLeaseFiles(t *testing.T) {
	open := func() *os.File {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	device, lease := open(), open()
	for _, msg := range []LeaseMessage{LeaseConnectors{Device: device}, LeaseReply{FD: lease}, LeaseConnectors{}, LeaseRevoke{}} {
		if err := CloseLeaseFiles(msg); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []*os.File{device, lease} {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("%v not closed: %v", f.Name(), err)
		}
	}
}

// The close error of a dropped message is returned to the caller.
func TestCloseLeaseFilesReturnsCloseError(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []LeaseMessage{LeaseConnectors{Device: f}, LeaseReply{FD: f}} {
		if err := CloseLeaseFiles(msg); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("%T: error = %v, want os.ErrClosed", msg, err)
		}
	}
}
