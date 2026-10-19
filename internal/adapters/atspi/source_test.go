package atspi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

const (
	stActive  = 1 << stateActive
	stShowing = 1 << stateShowing
)

// tree answers calls like an app: registry with one app (:1.5, pid 77) with
// one showing frame whose links sit at window-relative extents.
func answerTree(frameExt [4]int32, matches []ref, ext map[string][4]int32, roles map[string]uint32, noCollection bool) func(time.Time, time.Duration, []call) []reply {
	return func(_ time.Time, _ time.Duration, calls []call) []reply {
		out := make([]reply, len(calls))
		for i, c := range calls {
			switch {
			case c.op == opChildren && c.name == registryName:
				out[i].refs = []ref{{":1.5", "/root"}, {":1.9", "/root"}}
			case c.op == opPID:
				out[i].u = map[string]uint32{":1.5": 77, ":1.9": 99}[c.name]
			case c.op == opChildren && c.path == "/root":
				out[i].refs = []ref{{":1.5", "/frame"}}
			case c.op == opRole && c.path == "/frame":
				out[i].u = roleFrame
			case c.op == opState && c.path == "/frame":
				out[i].state = stShowing | stActive
			case c.op == opExtents && c.path == "/frame":
				out[i].rect = frameExt
			case c.op == opMatches:
				if noCollection {
					out[i].err = &busError{name: "org.freedesktop.DBus.Error.UnknownMethod"}
				} else {
					out[i].refs = matches
				}
			case c.op == opChildren && c.path == "/frame":
				out[i].refs = matches
			case c.op == opChildren:
			case c.op == opRole:
				out[i].u = roles[c.path]
			case c.op == opState:
				out[i].state = stShowing | 1<<stateVisible
			case c.op == opExtents:
				out[i].rect = ext[c.path]
			}
		}
		return out
	}
}

func TestTargetsCoversWindowAndConverts(t *testing.T) {
	tr := newMocktransport(t)
	matches := []ref{{":1.5", "/a"}, {":1.5", "/b"}, {":1.5", "/zero"}}
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{4, 30, 800, 600}, matches,
		map[string][4]int32{"/a": {14, 130, 50, 16}, "/b": {104, 230, 20, 20}, "/zero": {5, 5, 0, 0}},
		map[string]uint32{"/a": roleLink, "/b": rolePushButton, "/zero": roleLink}, false))
	s := &Source{tr: tr}

	windows := []ports.HintWindow{
		{ID: 1, PID: 77, Rect: ports.Rect{X: 100, Y: 200, W: 800, H: 600}, Focused: true},
		{ID: 2, PID: 5, Rect: ports.Rect{X: 0, Y: 0, W: 50, H: 50}},
	}
	targets, covered, err := s.Targets(context.Background(), windows, false)
	require.NoError(t, err)
	require.Equal(t, map[ports.WindowID]bool{1: true}, covered)
	require.Equal(t, []ports.HintTarget{
		{Rect: ports.Rect{X: 110, Y: 300, W: 50, H: 16}, Kind: ports.HintLink, Window: 1},
		{Rect: ports.Rect{X: 200, Y: 400, W: 20, H: 20}, Kind: ports.HintButton, Window: 1},
	}, targets)
}

func TestTargetsLinksOnlyDropsButtons(t *testing.T) {
	tr := newMocktransport(t)
	matches := []ref{{":1.5", "/a"}, {":1.5", "/b"}}
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, matches,
		map[string][4]int32{"/a": {14, 130, 50, 16}, "/b": {104, 230, 20, 20}},
		map[string]uint32{"/a": roleLink, "/b": rolePushButton}, false))
	s := &Source{tr: tr}
	targets, _, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1, PID: 77, Rect: ports.Rect{W: 800, H: 600}}}, true)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, ports.HintLink, targets[0].Kind)
}

func TestTargetsWalkWithoutCollection(t *testing.T) {
	tr := newMocktransport(t)
	matches := []ref{{":1.5", "/a"}}
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, matches,
		map[string][4]int32{"/a": {14, 130, 50, 16}},
		map[string]uint32{"/a": roleLink}, true))
	s := &Source{tr: tr}
	targets, covered, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1, PID: 77, Rect: ports.Rect{W: 800, H: 600}}}, false)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.True(t, covered[1])
}

func TestTargetsNoPIDMatchCoversNothing(t *testing.T) {
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree([4]int32{}, nil, nil, nil, false))
	s := &Source{tr: tr}
	targets, covered, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1, PID: 1234, Rect: ports.Rect{W: 8, H: 6}}}, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	require.Empty(t, covered)
}

func TestTargetsWithoutPIDsAsksNothing(t *testing.T) {
	s := &Source{tr: newMocktransport(t)}
	_, covered, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 8, H: 6}}}, false)
	require.NoError(t, err)
	require.Empty(t, covered)
}

func TestCoverageRequiresLinkOrDocument(t *testing.T) {
	for _, collection := range []bool{true, false} {
		for _, role := range []uint32{rolePushButton, roleDocumentFrame, roleDocumentWeb, roleLink} {
			t.Run(fmt.Sprintf("collection=%t/role=%d", collection, role), func(t *testing.T) {
				tr := newMocktransport(t)
				tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
					[4]int32{0, 0, 800, 600}, []ref{{":1.5", "/a"}}, map[string][4]int32{"/a": {10, 20, 30, 40}}, map[string]uint32{"/a": role}, !collection))
				s := &Source{tr: tr}
				targets, covered, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1, PID: 77, Rect: ports.Rect{W: 800, H: 600}}}, false)
				require.NoError(t, err)
				require.Equal(t, role != rolePushButton, covered[1])
				if isDocument(role) {
					require.Empty(t, targets)
				} else {
					require.Len(t, targets, 1)
				}
			})
		}
	}
}

var oneWindow = []ports.HintWindow{{ID: 1, PID: 77, Rect: ports.Rect{W: 800, H: 600}}}

// New does not touch the bus; the first Targets call connects, once.
func TestConnectsLazilyOnFirstTargets(t *testing.T) {
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, []ref{{":1.5", "/a"}}, map[string][4]int32{"/a": {1, 1, 9, 9}}, map[string]uint32{"/a": roleLink}, false))
	c := newMockconnector(t)
	c.EXPECT().Connect(mock.Anything).Return(tr, nil).Once()
	s := &Source{dial: c}
	for range 2 {
		targets, _, err := s.Targets(context.Background(), oneWindow, false)
		require.NoError(t, err)
		require.Len(t, targets, 1)
	}
}

// Without PIDs nothing is asked and nothing is connected.
func TestNoConnectWithoutWindows(t *testing.T) {
	s := &Source{dial: newMockconnector(t)}
	_, _, err := s.Targets(context.Background(), []ports.HintWindow{{ID: 1}}, false)
	require.NoError(t, err)
}

// A failed connection is retried no sooner than retryEvery; meanwhile no
// targets are returned.
func TestConnectRetriesWithBackoff(t *testing.T) {
	c := newMockconnector(t)
	c.EXPECT().Connect(mock.Anything).Return(nil, errors.New("down")).Once()
	s := &Source{dial: c}
	for range 3 {
		targets, covered, err := s.Targets(context.Background(), oneWindow, false)
		require.NoError(t, err)
		require.Empty(t, targets)
		require.Empty(t, covered)
	}
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, []ref{{":1.5", "/a"}}, map[string][4]int32{"/a": {1, 1, 9, 9}}, map[string]uint32{"/a": roleLink}, false))
	c.EXPECT().Connect(mock.Anything).Return(tr, nil).Once()
	s.retryAt = time.Now().Add(-time.Second)
	targets, _, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Len(t, targets, 1)
}

// A broken connection is closed and dropped; the next attempt reconnects.
func TestBrokenConnectionIsDropped(t *testing.T) {
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ time.Time, _ time.Duration, calls []call) []reply {
			out := make([]reply, len(calls))
			for i := range out {
				out[i].err = ErrBroken
			}
			return out
		}).Once()
	tr.EXPECT().Close().Return(nil).Once()
	c := newMockconnector(t)
	c.EXPECT().Connect(mock.Anything).Return(tr, nil).Once()
	s := &Source{dial: c}
	_, _, err := s.Targets(context.Background(), oneWindow, false)
	require.Error(t, err)
	require.Nil(t, s.tr)
	targets, _, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets) // backoff: no new attempt yet
}

const testWait = 20 * time.Millisecond

// blockedConnector returns a connector whose Connect blocks until release is
func blockedConnector(t *testing.T, release <-chan struct{}, tr transport, err error, times int) *mockconnector {
	t.Helper()
	c := newMockconnector(t)
	c.EXPECT().Connect(mock.Anything).RunAndReturn(func(context.Context) (transport, error) {
		<-release
		return tr, err
	}).Times(times)
	return c
}

// A silent service cannot hold Targets beyond the wait, is not doubled while
// in flight, and its late transport is used.
func TestConnectIsBoundedAndNotDoubled(t *testing.T) {
	release := make(chan struct{})
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, []ref{{":1.5", "/a"}}, map[string][4]int32{"/a": {1, 1, 9, 9}}, map[string]uint32{"/a": roleLink}, false))
	c := blockedConnector(t, release, tr, nil, 1)
	s := &Source{dial: c, wait: testWait}
	t0 := time.Now()
	targets, _, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	require.Less(t, time.Since(t0), testWait+200*time.Millisecond)
	require.NotNil(t, s.attempt)
	// Still in flight: no second Connect (Times(1)), and no blocking.
	targets, _, err = s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	// It connects late: the next call uses it.
	close(release)
	require.Eventually(t, func() bool { return len(s.attempt) == 1 }, time.Second, time.Millisecond)
	targets, _, err = s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Nil(t, s.attempt)
}

// An abandoned attempt that fails applies the backoff, then one new attempt.
func TestAbandonedAttemptErrorBacksOff(t *testing.T) {
	release := make(chan struct{})
	tr := newMocktransport(t)
	tr.EXPECT().Do(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(answerTree(
		[4]int32{0, 0, 800, 600}, []ref{{":1.5", "/a"}}, map[string][4]int32{"/a": {1, 1, 9, 9}}, map[string]uint32{"/a": roleLink}, false))
	c := newMockconnector(t)
	c.EXPECT().Connect(mock.Anything).RunAndReturn(func(context.Context) (transport, error) {
		<-release
		return nil, errors.New("down")
	}).Once()
	c.EXPECT().Connect(mock.Anything).Return(tr, nil).Once()
	s := &Source{dial: c, wait: testWait}
	targets, _, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	close(release)
	require.Eventually(t, func() bool { return len(s.attempt) == 1 }, time.Second, time.Millisecond)
	// The error is collected; the backoff runs from its finish time.
	targets, _, err = s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	require.Nil(t, s.attempt)
	require.WithinDuration(t, time.Now().Add(retryEvery), s.retryAt, time.Second)
	targets, _, err = s.Targets(context.Background(), oneWindow, false) // backoff: no Connect
	require.NoError(t, err)
	require.Empty(t, targets)
	s.retryAt = time.Now().Add(-time.Second)
	targets, _, err = s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Len(t, targets, 1)
}

// Close with an attempt in flight closes the late transport; afterwards
// Targets finds nothing and never connects.
func TestCloseReapsLateTransport(t *testing.T) {
	release := make(chan struct{})
	closed := make(chan struct{})
	tr := newMocktransport(t)
	tr.EXPECT().Close().RunAndReturn(func() error { close(closed); return nil }).Once()
	c := blockedConnector(t, release, tr, nil, 1)
	s := &Source{dial: c, wait: testWait}
	_, _, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("late transport not closed")
	}
	targets, covered, err := s.Targets(context.Background(), oneWindow, false)
	require.NoError(t, err)
	require.Empty(t, targets)
	require.Empty(t, covered)
	require.NoError(t, s.Close()) // idempotent
}
