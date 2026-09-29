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
	CloseLeaseFiles(LeaseConnectors{Device: device})
	CloseLeaseFiles(LeaseReply{FD: lease})
	CloseLeaseFiles(LeaseConnectors{})
	CloseLeaseFiles(LeaseRevoke{})
	for _, f := range []*os.File{device, lease} {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("%v not closed: %v", f.Name(), err)
		}
	}
}
