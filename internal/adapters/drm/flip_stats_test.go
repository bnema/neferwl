package drm

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

const period60 = 16_666_666 * time.Nanosecond // modeInfo{VRefresh: 60}

func flipAt(when time.Duration) flipEvent { return flipEvent{when: when} }

// p is the refresh period of the test output's 60 Hz mode.
const p = period60

// The gate and the counters of accountFlip, one flip after a frame flip at
// prev (1 s). due is the frame's due time: the later of wanted and gapEnd
// (see pendingFrame.dueAt; both 0: the commit time).
func TestAccountFlip(t *testing.T) {
	const prev = time.Second
	for _, tc := range []struct {
		name                string
		vrr                 bool
		prev                time.Duration // 0: no previous flip
		wanted, gapEnd      time.Duration
		commit, when, fence time.Duration
		want                flipStats
		wantLast            time.Duration
	}{
		{name: "first flip has no interval", prev: 0, wanted: prev, commit: prev, when: prev + p, want: flipStats{}, wantLast: prev + p},
		{name: "on time", prev: prev, wanted: prev + p/4, commit: prev + p/2, when: prev + p, want: flipStats{maxInterval: p, maxCommitDelay: p/2 - p/4}},
		{name: "unknown wanted time", prev: prev, commit: prev + p/2, when: prev + p, want: flipStats{maxInterval: p}},
		{name: "just under 1.5 periods", prev: prev, wanted: prev, commit: prev + p/2, when: prev + p*3/2 - 1, want: flipStats{maxInterval: p*3/2 - 1, maxCommitDelay: p / 2}},
		{name: "two periods is a missed vblank", prev: prev, wanted: prev, commit: prev + p/2, when: prev + 2*p, want: flipStats{missedVblanks: 1, maxInterval: 2 * p, maxCommitDelay: p / 2}},
		{name: "idle gap is not counted", prev: prev, wanted: prev + time.Second, commit: prev + time.Second + p/2, when: prev + time.Second + p, want: flipStats{}},
		{name: "wanted one period after: not due", prev: prev, wanted: prev + p, commit: prev + p + p/2, when: prev + 5*p, want: flipStats{}},
		{name: "wanted just before one period: due", prev: prev, wanted: prev + p - 1, commit: prev + p + p/2, when: prev + 4*p, want: flipStats{missedVblanks: 1, maxInterval: 4 * p, maxCommitDelay: p/2 + 1}},
		{name: "delayed commit, wanted before the previous flip", prev: prev, wanted: prev - p/2, commit: prev + p + p/5, when: prev + 2*p, fence: prev + p/2,
			want: flipStats{missedVblanks: 1, maxInterval: 2 * p, maxCommitDelay: p + p/5}},
		{name: "normal frame: fence before its flip", prev: prev, wanted: prev + p/4, commit: prev + p/2, when: prev + p, fence: prev + p - 1,
			want: flipStats{maxInterval: p, maxCommitDelay: p/2 - p/4}},
		{name: "missed, fence unknown", prev: prev, wanted: prev, commit: prev + p/2, when: prev + 2*p, want: flipStats{missedVblanks: 1, maxInterval: 2 * p, maxCommitDelay: p / 2}},
		{name: "missed, fence at the targeted vblank", prev: prev, wanted: prev, commit: prev + p/2, when: prev + 2*p, fence: prev + p,
			want: flipStats{missedVblanks: 1, maxInterval: 2 * p, maxCommitDelay: p / 2}},
		{name: "missed, fence after the targeted vblank", prev: prev, wanted: prev, commit: prev + p/2, when: prev + 2*p, fence: prev + p + 1,
			want: flipStats{missedVblanks: 1, lateFences: 1, maxInterval: 2 * p, maxCommitDelay: p / 2}},
		{name: "VRR idle gap", vrr: true, prev: prev, wanted: prev + time.Second, commit: prev + time.Second + time.Millisecond, when: prev + time.Second + 2*time.Millisecond, want: flipStats{}},
		{name: "VRR back to back: no missed vblank, no late fence", vrr: true, prev: prev, wanted: prev, commit: prev + p/2, when: prev + 3*p, fence: prev + 3*p - 1,
			want: flipStats{maxInterval: 3 * p, maxCommitDelay: p / 2}},
		{name: "VRR flip gap is not a commit delay", vrr: true, prev: prev, wanted: prev, gapEnd: prev + 2*time.Millisecond, commit: prev + 2*time.Millisecond + 100*time.Microsecond, when: prev + 3*time.Millisecond,
			want: flipStats{maxInterval: 3 * time.Millisecond, maxCommitDelay: 100 * time.Microsecond}},
		{name: "wanted after the gap ended", vrr: true, prev: prev, wanted: prev + 5*time.Millisecond, gapEnd: prev + 2*time.Millisecond, commit: prev + 6*time.Millisecond, when: prev + 7*time.Millisecond,
			want: flipStats{maxInterval: 7 * time.Millisecond, maxCommitDelay: time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := testOutput(t)
			o.vrrOn, o.lastFlipAt = tc.vrr, tc.prev
			f := pendingFrame{wantedAt: tc.wanted, gapEnd: tc.gapEnd}
			o.accountFlip(flipAt(tc.when), true, f.dueAt(), tc.commit, tc.fence)
			if o.flipStats != tc.want {
				t.Fatalf("stats %+v, want %+v", o.flipStats, tc.want)
			}
			wantLast := tc.wantLast
			if wantLast == 0 {
				wantLast = tc.when
			}
			if o.lastFlipAt != wantLast {
				t.Fatalf("last flip %s, want %s", o.lastFlipAt, wantLast)
			}
		})
	}
}

// The gap end of the flip stats runs from the flip's kernel timestamp, the
// commit gate from the time the event was read: an event read long after
// the flip still shows as a commit delay.
func TestStartFlipGapFromFlipTimestamp(t *testing.T) {
	o, _, _ := testOutput(t)
	gap := 2 * time.Millisecond
	o.vrrFlipGap, o.vrrOn, o.vrrGame = gap, true, true
	flipped := time.Hour // CLOCK_MONOTONIC of the flip
	readAt := time.Unix(1000, 0)
	o.startFlipGap(pendingFrame{frame: true}, flipped, readAt)
	if o.flipGapAt != flipped+gap || !o.flipGapUntil.Equal(readAt.Add(gap)) {
		t.Fatalf("gap at %s until %s", o.flipGapAt, o.flipGapUntil)
	}
	// The event was read 30 ms after the flip (> one period), the next
	// frame committed right after: the delay is not hidden in the gap.
	o.lastFlipAt = flipped
	f := pendingFrame{wantedAt: flipped + time.Millisecond, gapEnd: o.flipGapAt}
	o.accountFlip(flipAt(flipped+2*p), true, f.dueAt(), flipped+30*time.Millisecond, 0)
	if want := 30*time.Millisecond - gap; o.flipStats.maxCommitDelay != want {
		t.Fatalf("commit delay %s, want %s", o.flipStats.maxCommitDelay, want)
	}
}

// The gap end set at a game frame's flip travels with the next frame
// commit, which consumes it.
func TestFlipGapEndReachesNextFrame(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	o.vrrFlipGap, o.vrrOn, o.vrrGame = 2*time.Millisecond, true, true
	seen := map[ports.WindowID]uint64{}
	if err := o.commitFrame(70, nil, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	ev := eventOf((*commits)[0])
	ev.when = monotonic()
	o.completed(ev, seen)
	if o.flipGapAt != ev.when+2*time.Millisecond {
		t.Fatalf("gap end %s, flip at %s", o.flipGapAt, ev.when)
	}
	gap := o.flipGapAt
	if err := o.commitFrame(71, nil, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if o.frame.pendingFrame.gapEnd != gap || o.flipGapAt != 0 {
		t.Fatalf("gap end %s, left %s, want %s", o.frame.pendingFrame.gapEnd, o.flipGapAt, gap)
	}
}

// A VRR change starts a new interval chain, in both directions.
func TestSetVRRRestartsFlipInterval(t *testing.T) {
	o, _, _ := testOutput(t)
	o.lastFlipAt = time.Second
	o.setVRR(false)
	if o.lastFlipAt != time.Second {
		t.Fatal("unchanged state reset the chain")
	}
	o.setVRR(true)
	if o.lastFlipAt != 0 || !o.vrrOn {
		t.Fatalf("vrr on: last %s", o.lastFlipAt)
	}
	o.lastFlipAt = time.Second
	o.setVRR(false)
	if o.lastFlipAt != 0 || o.vrrOn {
		t.Fatalf("vrr off: last %s", o.lastFlipAt)
	}
}

// A state commit (cursor, VRR) is neither a frame interval nor a frame.
func TestAccountFlipStateCommit(t *testing.T) {
	o, _, _ := testOutput(t)
	o.lastFlipAt = time.Second
	o.accountFlip(flipAt(time.Second+3*p), false, time.Second+p/2, time.Second+p/2, time.Second+3*p)
	if s := o.flipStats; s != (flipStats{}) || o.lastFlipAt != time.Second {
		t.Fatalf("stats %+v last %s", s, o.lastFlipAt)
	}
}

func TestAccountReadMax(t *testing.T) {
	o, _, _ := testOutput(t)
	for _, d := range []time.Duration{2 * time.Millisecond, 25 * time.Millisecond, 5 * time.Millisecond} {
		o.accountRead(flipAt(time.Second), time.Second+d)
	}
	if got := o.flipStats.maxFlipToRead; got != 25*time.Millisecond {
		t.Fatalf("max flip to read %s", got)
	}
}

func TestTakeFlipStatsResets(t *testing.T) {
	o, _, _ := testOutput(t)
	o.lastFlipAt = time.Second
	// Missed, with a fence after the targeted vblank.
	o.accountFlip(flipAt(time.Second+3*p), true, time.Second, time.Second+p/2, time.Second+2*p)
	o.accountRead(flipAt(time.Second), time.Second+time.Millisecond)
	o.accountFlip(flipAt(time.Second+4*p), true, time.Second+3*p, time.Second+3*p+p/2, 0)
	s := o.takeFlipStats()
	if s.missedVblanks != 1 || s.lateFences != 1 || s.maxInterval != 3*p || s.maxCommitDelay != p/2 || s.maxFlipToRead != time.Millisecond {
		t.Fatalf("taken %+v", s)
	}
	if o.flipStats != (flipStats{}) {
		t.Fatalf("not reset: %+v", o.flipStats)
	}
	// The interval chain survives the report.
	if o.lastFlipAt != time.Second+4*p {
		t.Fatalf("last flip %s", o.lastFlipAt)
	}
}

// Every frame flip keeps lastFlipAt, traced or not, and our commit's
// event delivery is measured.
func TestCompletedKeepsLastFlipWithoutTrace(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if !o.completed(flipEvent{crtc: tCrtc, user: (*commits)[0].user, when: 5 * time.Second}, map[ports.WindowID]uint64{}) {
		t.Fatal("not completed")
	}
	if o.lastFlipAt != 5*time.Second {
		t.Fatalf("last flip %s", o.lastFlipAt)
	}
	if o.flipStats.maxFlipToRead <= 0 {
		t.Fatalf("flip to read not measured: %+v", o.flipStats)
	}
}

// The end of an EBUSY wait and a stale event are not deliveries of our
// commit: they do not feed max_flip_to_read.
func TestCompletedReadLatencyOnlyOurCommit(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	seen := map[ports.WindowID]uint64{}
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	o.completed(flipEvent{crtc: tCrtc, user: c.user + 1<<userKindBits}, seen) // stale
	if o.flipStats.maxFlipToRead != 0 {
		t.Fatalf("stale event measured: %+v", o.flipStats)
	}
	o.frame.busy(time.Now())
	if !o.completed(flipEvent{crtc: tCrtc, user: 12345}, seen) || o.flipStats.maxFlipToRead != 0 {
		t.Fatalf("EBUSY wait end measured: %+v", o.flipStats)
	}
}

// With tracing on, completed still logs the interval since the previous
// frame flip, and moves lastFlipAt after it.
func TestCompletedTracesFlipInterval(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	o.traceFlips = true
	var log bytes.Buffer
	o.log = zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &log})
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	when := monotonic()
	o.lastFlipAt = when - 10*time.Millisecond
	if !o.completed(flipEvent{crtc: tCrtc, user: (*commits)[0].user, when: when}, map[ports.WindowID]uint64{}) {
		t.Fatal("not completed")
	}
	if !strings.Contains(log.String(), `"flip_interval_ms":10`) || o.lastFlipAt != when {
		t.Fatalf("last flip %s: %s", o.lastFlipAt, log.String())
	}
}

func TestAccountFlipAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	at := time.Second
	if n := testing.AllocsPerRun(100, func() {
		at += 40 * time.Millisecond
		o.accountFlip(flipAt(at), true, at-35*time.Millisecond, at-30*time.Millisecond, at-20*time.Millisecond)
		o.accountFlip(flipAt(at), false, at, at, 0)
		o.accountRead(flipAt(at), at+time.Millisecond)
		_ = o.takeFlipStats()
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// The completion accounting of a frame flip, tracing off, in steady state.
// (completed itself reports the flip to wayland, which allocates.)
func TestFlipDoneAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	o.vrrFlipGap, o.vrrOn, o.vrrGame = time.Millisecond, true, true
	start := time.Now()
	at := time.Second
	if n := testing.AllocsPerRun(100, func() {
		at += 10 * time.Millisecond
		o.flipDone(flipAt(at), pendingFrame{frame: true, wantedAt: at - time.Millisecond}, start, true, false, 0)
	}); n != 0 {
		t.Fatalf("flipDone: %v allocations", n)
	}
}
