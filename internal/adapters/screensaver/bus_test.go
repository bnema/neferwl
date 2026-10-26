package screensaver

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

func TestRunStartupFailures(t *testing.T) {
	failure := errors.New("connection failed")
	for _, tt := range []struct {
		name                       string
		owner                      uint32
		requestErr, matchErr, want error
	}{
		{name: "name taken", owner: zerobus.NameExists, want: ErrNameTaken},
		{name: "request fails", requestErr: failure, want: failure},
		{name: "match fails", owner: zerobus.NamePrimaryOwner, matchErr: failure, want: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newMockbus(t)
			match := b.EXPECT().AddMatch("type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg2=''").Return(tt.matchErr).Once()
			if tt.matchErr == nil {
				request := b.EXPECT().RequestName(busName, uint32(zerobus.NameFlagDoNotQueue)).Return(tt.owner, tt.requestErr).Once()
				mock.InOrder(match, request)
			}
			b.EXPECT().Close().Return(nil).Once()
			s := Service{conn: b, log: logging.For(context.Background(), "screensaver")}
			if err := s.Run(context.Background(), Reports{}); !errors.Is(err, tt.want) {
				t.Fatalf("Run: %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDispatchReplies(t *testing.T) {
	for _, tt := range []struct{ name, member, signature, replySig, errorName string }{
		{name: "active", member: "GetActive", replySig: "b"},
		{name: "time", member: "GetActiveTime", replySig: "u"},
		{name: "activity", member: "SimulateUserActivity"},
		{name: "unknown", member: "NoSuchMethod", errorName: "UnknownMethod"},
		{name: "invalid arguments", member: "Inhibit", signature: "u", errorName: "InvalidArgs"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := newMockbus(t)
			m := &zerobus.Message{Type: zerobus.TypeMethodCall, Sender: ":1.9", Serial: 7, Path: "/ScreenSaver", Interface: iface, Member: tt.member, Signature: tt.signature}
			encoder := new(zerobus.Encoder)
			if tt.errorName != "" {
				b.EXPECT().NewError(m, "org.freedesktop.DBus.Error."+tt.errorName, "s").Return(encoder).Once()
			} else {
				b.EXPECT().NewReply(m, tt.replySig).Return(encoder).Once()
			}
			failure := errors.New("send failed")
			b.EXPECT().Send().Return(uint32(0), failure).Once()
			activity := make(chan struct{}, 1)
			s := Service{conn: b}
			_, _, err := s.dispatch(m, newRegistry(), activity)
			if !errors.Is(err, failure) {
				t.Fatalf("dispatch: %v", err)
			}
			if tt.member == "SimulateUserActivity" && len(activity) != 1 {
				t.Fatal("no activity")
			}
		})
	}
}

func TestNoReplyActivity(t *testing.T) {
	b := newMockbus(t)
	s := Service{conn: b}
	activity := make(chan struct{}, 1)
	m := &zerobus.Message{Sender: ":1.9", Path: "/ScreenSaver", Interface: iface, Member: "SimulateUserActivity", Flags: zerobus.FlagNoReplyExpected}
	for range 3 {
		if _, _, err := s.dispatch(m, newRegistry(), activity); err != nil {
			t.Fatal(err)
		}
	}
	if len(activity) != 1 {
		t.Fatal("activity not coalesced")
	}
}

// Cancellation closes a connection blocked in startup, then joins the watcher.
func TestRunCanceledDuringStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		b := newMockbus(t)
		closed := make(chan struct{})
		b.EXPECT().AddMatch("type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg2=''").Return(nil).Once()
		b.EXPECT().RequestName(busName, uint32(zerobus.NameFlagDoNotQueue)).RunAndReturn(func(string, uint32) (uint32, error) {
			cancel()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				return 0, errors.New("close timed out")
			}
			return 0, zerobus.ErrClosed
		}).Once()
		b.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
		b.EXPECT().Close().Return(nil).Once()
		s := Service{conn: b, log: logging.For(ctx, "screensaver")}
		if err := s.Run(ctx, Reports{}); err != nil {
			t.Fatal(err)
		}
	})
}
