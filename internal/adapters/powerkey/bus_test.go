package powerkey

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

func TestHoldBusFailuresPreserveLock(t *testing.T) {
	failure := errors.New("bus failed")
	for _, stage := range []string{"fds", "match", "send", "read", "reply"} {
		t.Run(stage, func(t *testing.T) {
			b := newMockbus(t)
			b.EXPECT().UnixFDs().Return(stage != "fds").Once()
			b.EXPECT().Close().Return(nil).Once()
			if stage != "fds" {
				var matchErr error
				if stage == "match" {
					matchErr = failure
				}
				b.EXPECT().AddMatch(mock.Anything).Return(matchErr).Once()
				if stage != "match" {
					b.EXPECT().NewMethodCall(logind, logindPath, manager, "Inhibit", "ssss").Return(new(zerobus.Encoder), uint32(7)).Once()
					var sendErr error
					if stage == "send" {
						sendErr = failure
					}
					b.EXPECT().Send().Return(uint32(7), sendErr).Once()
					if stage == "read" {
						b.EXPECT().ReadMessage().Return(nil, failure).Once()
					}
					if stage == "reply" {
						b.EXPECT().ReadMessage().Return(&zerobus.Message{Type: zerobus.TypeMethodReturn, ReplySerial: 7, Signature: "s"}, nil).Once()
					}
				}
			}
			old, err := os.CreateTemp(t.TempDir(), "lock")
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			lock := old
			if err := holdBus(context.Background(), b, logging.For(context.Background(), "powerkey"), &lock); err == nil {
				t.Fatal("expected error")
			}
			if lock != old {
				t.Fatal("replaced old lock on failure")
			}
			if _, err := old.Stat(); err != nil {
				t.Fatal("closed old lock on failure:", err)
			}
		})
	}
}

func TestHoldBusCanceledDuringMatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newMockbus(t)
	closed := make(chan struct{})
	b.EXPECT().UnixFDs().Return(true).Once()
	b.EXPECT().AddMatch(mock.Anything).RunAndReturn(func(string) error { cancel(); <-closed; return zerobus.ErrClosed }).Once()
	b.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
	b.EXPECT().Close().Return(nil).Once()
	var lock *os.File
	if err := holdBus(ctx, b, logging.For(ctx, "powerkey"), &lock); err != nil {
		t.Fatal(err)
	}
}

func TestHoldBusInhibitTimeout(t *testing.T) {
	b := newMockbus(t)
	closed := make(chan struct{})
	b.EXPECT().UnixFDs().Return(true).Once()
	b.EXPECT().AddMatch(mock.Anything).Return(nil).Once()
	b.EXPECT().NewMethodCall(logind, logindPath, manager, "Inhibit", "ssss").Return(new(zerobus.Encoder), uint32(7)).Once()
	b.EXPECT().Send().Return(uint32(7), nil).Once()
	b.EXPECT().ReadMessage().RunAndReturn(func() (*zerobus.Message, error) { <-closed; return nil, zerobus.ErrClosed }).Once()
	b.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
	b.EXPECT().Close().Return(nil).Once()
	var lock *os.File
	if err := holdBus(context.Background(), b, logging.For(context.Background(), "powerkey"), &lock); !errors.Is(err, zerobus.ErrClosed) {
		t.Fatalf("timeout: %v", err)
	}
}
