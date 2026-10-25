package powerkey

import (
	"context"

	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// A peer that accepts but never answers authentication must not hold shutdown.
func TestDialCancellationDuringAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		Serve(ctx, "unix:path="+path, time.Hour, logging.For(ctx, "powerkey"))
		done <- nil
	}()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		} else {
			accepted <- nil
		}
	}()
	var conn net.Conn
	select {
	case conn = <-accepted:
		if conn == nil {
			t.Fatal("accept failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dial did not connect")
	}
	defer conn.Close()
	// Wait for auth bytes, so cancellation exercises the handshake, not just connect.
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt authentication")
	}
}
