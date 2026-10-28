package screencast

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

func TestAcquireCancelledPendingReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b := newMockbus(t)
		s := &Service{ctx: ctx, conn: b}
		b.EXPECT().AddMatch(mock.Anything).Return(nil).Once()
		b.EXPECT().NewMethodCall(busDaemon, busDaemonPath, busDaemon, "RequestName", "su").Return(new(zerobus.Encoder), uint32(42)).Once()
		b.EXPECT().Send().Return(uint32(42), nil).Once()
		closed := make(chan struct{})
		b.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
		b.EXPECT().ReadMessage().RunAndReturn(func() (*zerobus.Message, error) {
			<-closed
			return nil, zerobus.ErrClosed
		}).Once()
		done := make(chan error, 1)
		go func() { done <- s.acquire() }()
		synctest.Wait() // RequestName has been sent; the reply read is blocked.
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("acquire: %v", err)
			}
		default:
			// Unblock the mutant too, so a missing cancellation hook fails without
			// leaving a blocked goroutine in the bubble.
			s.closeBus()
			<-done
			t.Fatal("acquire outlived cancellation with a reply pending")
		}
		s.closeBus() // Owner cleanup must not close the connection again.
	})
}

func TestAcquireSuccessStopsCancellationHook(t *testing.T) {
	addr, _ := startBus(t)
	conn, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer boundIO(t, "name reply", func() { _ = conn.Close() })()
	e, serial := conn.NewMethodCall(busDaemon, busDaemonPath, busDaemon, "RequestName", "su")
	e.Str(nameDisplayConfig)
	e.Uint32(zerobus.NameFlagDoNotQueue)
	if _, err := conn.Send(); err != nil {
		t.Fatal(err)
	}
	var reply *zerobus.Message
	for {
		m, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.ReplySerial == serial {
			reply = m
			break
		}
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b := newMockbus(t)
		s := &Service{ctx: ctx, conn: b, log: testLog(&logBuf{})}
		b.EXPECT().AddMatch(mock.Anything).Return(nil).Once()
		b.EXPECT().NewMethodCall(busDaemon, busDaemonPath, busDaemon, "RequestName", "su").Return(new(zerobus.Encoder), serial).Times(3)
		b.EXPECT().Send().Return(serial, nil).Times(3)
		b.EXPECT().ReadMessage().Return(reply, nil).Times(3)
		if err := s.acquire(); err != nil {
			t.Fatal(err)
		}
		if s.owns != [4]bool{false, true, true, true} {
			t.Fatalf("owned names: %v", s.owns)
		}
		cancel()
		synctest.Wait()
		b.AssertNotCalled(t, "Close")
	})
}
