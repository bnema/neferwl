package wayland

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// Adjacent motions for one window fold into the last queued command, summing
// the relative deltas; nothing is merged across another command, and the
// order of arrival is kept.
func TestCommandRingCollapsesMotionInOrder(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	locked := ports.SecurityState{Generation: 2, Protected: true}
	unlocked := ports.SecurityState{Generation: 3}
	var got []ports.ClientCommand // display goroutine only
	r := newCommandRing(s.display, func(q queuedCommand) { got = append(got, q.command()) })
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled
	for _, c := range []ports.ClientCommand{
		ports.PointerMotionTo{ID: 1, X: 1, DX: 0.25, UnaccelDX: 0.5},
		ports.PointerMotionTo{ID: 1, X: 2, DX: 0.25, UnaccelDX: 0.5},
		ports.PointerMotionTo{ID: 1, X: 3, DX: 0.25, DY: -0.5, UnaccelDX: 0.5, UnaccelDY: -1},
		ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 4},
		ports.PointerFocus{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 5},
		ports.PointerMotionTo{ID: 2, X: 6},
		ports.PointerMotionTo{ID: 1, X: 7}, // other window: not adjacent-same
		// Enveloped, as core sends them: same state merges, a different
		// state, a bare motion, or another window does not.
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 8, DX: 1, UnaccelDY: 2}},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 9, DX: 2, UnaccelDY: 3}},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 10, DY: 4}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 11, DX: 1}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 12, DX: 1}},
		ports.PointerMotionTo{ID: 1, X: 13},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 14}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 2, X: 15}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerButtonTo{ID: 2}},
	} {
		if !r.Push(queue(c)) {
			t.Fatal("push refused")
		}
	}
	close(release)
	want := []ports.ClientCommand{
		// Slow motions add up: none of the sub-pixel deltas is lost.
		ports.PointerMotionTo{ID: 1, X: 3, DX: 0.75, DY: -0.5, UnaccelDX: 1.5, UnaccelDY: -1},
		ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 4},
		ports.PointerFocus{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 6},
		ports.PointerMotionTo{ID: 1, X: 7},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 10, DX: 3, DY: 4, UnaccelDY: 5}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 12, DX: 2}},
		ports.PointerMotionTo{ID: 1, X: 13},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 14}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 2, X: 15}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerButtonTo{ID: 2}},
	}
	var final []ports.ClientCommand
	s.display.Do(func() { final = got }) // runs after the drain posted before it
	if !reflect.DeepEqual(final, want) {
		t.Fatalf("applied %#v, want %#v", final, want)
	}
}

// A merge needs no room: motion keeps folding into the last queued command
// while the ring is full of other commands, and the push does not block.
func TestCommandRingMergesWhenFull(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	var got []ports.ClientCommand
	r := newCommandRing(s.display, func(q queuedCommand) { got = append(got, q.command()) })
	release := releaseOnce(t)
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release.ch })
	<-stalled
	for i := range commandRingSize - 1 {
		r.Push(queue(ports.PointerFocus{ID: ports.WindowID(i)}))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 { // fills the ring with one motion, then folds the rest
			r.Push(queue(ports.PointerMotionTo{ID: 1, DX: 1}))
		}
	}()
	select {
	case <-done:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("motion push blocked on a full ring")
	}
	release.do()
	var last ports.ClientCommand
	s.display.Do(func() { last = got[len(got)-1] })
	if m, ok := last.(ports.PointerMotionTo); !ok || m.DX != 1000 {
		t.Fatalf("last applied %#v, want one motion with DX 1000", last)
	}
}

// A busy display goroutine must not stall the forwarder: it keeps draining
// the Commands channel into the ring up to the ring capacity, and only then
// does it wait (and core with it, through the full channel).
func TestForwarderDrainsWhileDisplayBusy(t *testing.T) {
	cmds := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, Channels{Commands: cmds}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	defer func() { cancel(); <-done }()

	// Deferred after the shutdown above, so it runs first: a failing
	// assertion never leaves the display stalled while Run is joined.
	release := releaseOnce(t)
	defer release.do()
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release.ch })
	<-stalled

	// Not mergeable: every command takes a slot.
	const total = commandRingSize + 100
	filled := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range total {
			select {
			case cmds <- ports.PointerFocus{ID: ports.WindowID(i + 1)}:
				if i+1 == commandRingSize+1+cap(cmds) {
					close(filled)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	queued := func() int {
		s.commands.mu.Lock()
		defer s.commands.mu.Unlock()
		return len(s.commands.ring)
	}
	// Ring full, one command in the blocked forwarder, a full channel.
	select {
	case <-filled:
	case <-waylandDeadline(5 * time.Second):
		t.Fatal("sender did not fill channel and ring")
	}
	waitPushWaiting(t)
	if len(cmds) != cap(cmds) {
		t.Fatalf("channel %d, want %d", len(cmds), cap(cmds))
	}
	// The forwarder is blocked in Push only after filling the ring.
	if queued() != commandRingSize {
		t.Fatalf("ring holds %d, want %d", queued(), commandRingSize)
	}
	select {
	case <-sent:
		t.Fatal("sender finished although the ring is full")
	default:
	}
	release.do()
	select {
	case <-sent:
	case <-waylandDeadline(5 * time.Second):
		t.Fatal("forwarder did not resume after the display was released")
	}
}

// Each command used to cost one display.Do round trip (up to one 5 ms
// event-loop dispatch), capping pointer motion at ~150/s. A 1 kHz mouse
// must be absorbed without core blocking.
func TestCommandThroughput(t *testing.T) {
	cmds := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, Channels{Commands: cmds}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	for i := range 2000 {
		cmds <- ports.PointerMotionTo{ID: 1, X: float64(i), Y: 1}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("2000 motion commands took %v (%.0f/s); want well above 1000/s", d, 2000/d.Seconds())
	}
}

// release unblocks a stalled display goroutine once, from the test or its
// cleanup, so a failed assertion never hangs the server shutdown.
type release struct {
	ch   chan struct{}
	once sync.Once
}

func (r *release) do() { r.once.Do(func() { close(r.ch) }) }

// releaseOnce returns a release that the test cleanup also fires, before
// the cleanups registered earlier (the server's).
func releaseOnce(t *testing.T) *release {
	r := &release{ch: make(chan struct{})}
	t.Cleanup(r.do)
	return r
}

// A merged motion keeps the push time of the first motion folded into it, so
// the reported latency is the longest wait any of its motions had.
func TestMergeMotionKeepsOldestPushTime(t *testing.T) {
	t0 := time.Unix(100, 0)
	first := queue(ports.PointerMotionTo{ID: 1, DX: 1})
	first.at = t0
	second := queue(ports.PointerMotionTo{ID: 1, DX: 2})
	second.at = t0.Add(5 * time.Millisecond)
	if !mergeMotion(&first, second) {
		t.Fatal("adjacent motions did not merge")
	}
	if !first.at.Equal(t0) {
		t.Fatalf("merged at = %v, want the oldest %v", first.at, t0)
	}
}

// commandStats keeps the largest push-to-apply wait and the applied count,
// and take resets both.
func TestCommandStatsObserveAndTake(t *testing.T) {
	var c commandStats
	t0 := time.Unix(100, 0)
	c.observe(t0, t0.Add(3*time.Millisecond))
	c.observe(t0, t0.Add(9*time.Millisecond))
	c.observe(t0, t0.Add(time.Millisecond))
	if n, d := c.take(); n != 3 || d != 9*time.Millisecond {
		t.Fatalf("take = %d, %v; want 3, 9ms", n, d)
	}
	if n, d := c.take(); n != 0 || d != 0 {
		t.Fatalf("second take = %d, %v; want zeros", n, d)
	}
}

// The ring counts the largest queued length and the folded pushes, and
// takeStats returns then resets them.
func TestCommandRingStats(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	r := newCommandRing(s.display, func(queuedCommand) {})
	release := releaseOnce(t)
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release.ch })
	<-stalled
	r.Push(queue(ports.PointerFocus{ID: 1}))
	r.Push(queue(ports.PointerMotionTo{ID: 1, DX: 1}))
	r.Push(queue(ports.PointerMotionTo{ID: 1, DX: 1})) // folded
	r.Push(queue(ports.PointerMotionTo{ID: 1, DX: 1})) // folded
	r.Push(queue(ports.PointerFocus{ID: 2}))
	st := r.takeStats()
	if st.maxLen != 3 || st.merged != 2 || st.maxPushBlock != 0 {
		t.Fatalf("stats = %+v, want maxLen 3, merged 2, no block", st)
	}
	if st := r.takeStats(); st != (ringStats{maxLen: 3}) {
		t.Fatalf("stats after take = %+v, want current occupancy only", st)
	}
	release.do()
}

// The report logs the period's figures and resets them; a period without
// applied commands logs nothing.
func TestReportCommandStats(t *testing.T) {
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
	h := newAdmissionHarnessWith(t, 1, Options{}, log)
	s := h.s
	s.reportCommandStats()
	if strings.Contains(out.String(), "command stats") {
		t.Fatalf("idle period logged: %s", out.String())
	}
	t0 := time.Unix(100, 0)
	s.commandStats.observe(t0, t0.Add(1500*time.Microsecond))
	s.reportCommandStats()
	got := out.String()
	for _, want := range []string{`"message":"command stats"`, `"commands":1`, `"merged":0`, `"max_queued":0`, `"max_push_block_ms":0`, `"max_latency_ms":1.5`} {
		if !strings.Contains(got, want) {
			t.Fatalf("log lacks %s: %s", want, got)
		}
	}
	n := strings.Count(got, "command stats")
	s.reportCommandStats()
	if strings.Count(out.String(), "command stats") != n {
		t.Fatalf("stats not reset: %s", out.String())
	}
}

// A stalled display applies nothing but fills its ring: the period still
// logs, so a long stall is not hidden.
func TestReportCommandStatsDuringStall(t *testing.T) {
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
	h := newAdmissionHarnessWith(t, 1, Options{}, log)
	s := h.s
	s.commands.mu.Lock()
	s.commands.stats.maxLen = 5
	s.commands.mu.Unlock()
	s.reportCommandStats()
	got := out.String()
	for _, want := range []string{`"message":"command stats"`, `"commands":0`, `"max_queued":5`} {
		if !strings.Contains(got, want) {
			t.Fatalf("log lacks %s: %s", want, got)
		}
	}
}
