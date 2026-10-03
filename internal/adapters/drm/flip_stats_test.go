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

// flip is a frame flip at when whose commit was made at commit, for a
// frame wanted at the same time.
func flip(o *Output, when, commit, fenceAt time.Duration) {
	o.accountFlip(flipAt(when), true, commit, commit, fenceAt)
}

// flipWanted is flip for a frame wanted at wanted.
func flipWanted(o *Output, when, wanted, commit, fenceAt time.Duration) {
	o.accountFlip(flipAt(when), true, wanted, commit, fenceAt)
}

func TestAccountFlipMissedVblanks(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	if p < period60-time.Microsecond || p > period60+time.Microsecond {
		t.Fatalf("period %s", p)
	}
	at := time.Second
	// The first flip has no interval.
	flip(o, at, at-p, 0)
	if s := o.flipStats; s.missedVblanks != 0 || s.maxInterval != 0 || o.lastFlipAt != at {
		t.Fatalf("first flip: %+v last %s", s, o.lastFlipAt)
	}
	// On time, and just under 1.5 periods: not missed.
	flip(o, at+p, at+p/2, 0)
	at += p
	flip(o, at+p*3/2-1, at+p/2, 0)
	at += p * 3 / 2
	if o.flipStats.missedVblanks != 0 {
		t.Fatalf("on time counted: %+v", o.flipStats)
	}
	// Committed back to back, flipped 2 then 3 periods later: missed.
	flip(o, at+2*p, at+p/2, 0)
	at += 2 * p
	flip(o, at+3*p, at+p/2, 0)
	at += 3 * p
	flip(o, at+p, at+p/2, 0)
	if s := o.flipStats; s.missedVblanks != 2 || s.maxInterval != 3*p {
		t.Fatalf("stats %+v", s)
	}
}

// An output commits on change only: the gap after an idle period is not
// a missed vblank, whatever the flip interval.
func TestAccountFlipIdleGapNotCounted(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	flip(o, time.Second, time.Second-p, 0)
	// Committed a second after the previous flip: nothing was waiting.
	flip(o, 2*time.Second, 2*time.Second-p/2, 2*time.Second-p)
	s := o.flipStats
	if s.missedVblanks != 0 || s.maxInterval != 0 || s.lateFences != 0 || o.lastFlipAt != 2*time.Second {
		t.Fatalf("idle gap counted: %+v", s)
	}
	// Committed exactly one period after: it could not make the vblank.
	flip(o, 2*time.Second+5*p, 2*time.Second+p, 0)
	if s := o.flipStats; s.missedVblanks != 0 || s.maxInterval != 0 {
		t.Fatalf("commit after the next vblank counted: %+v", s)
	}
	// Committed just before it, flipped 4 periods later: counted.
	flip(o, 2*time.Second+9*p, 2*time.Second+5*p+p-1, 0)
	if s := o.flipStats; s.missedVblanks != 1 || s.maxInterval != 4*p {
		t.Fatalf("back-to-back late flip: %+v", s)
	}
}

// A frame wanted while the previous flip was still in flight is due at
// that flip. If the event is read late and the commit comes after the
// next vblank, the frame is still counted, with its commit delay.
func TestAccountFlipDelayedCommitCounted(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	at := time.Second
	flip(o, at, at-p, 0)
	// Wanted before the previous flip landed, committed 1.2 periods
	// after it (late read), flipped two vblanks after it.
	flipWanted(o, at+2*p, at-p/2, at+p+p/5, at+p/2)
	s := o.flipStats
	if s.missedVblanks != 1 || s.maxInterval != 2*p || s.maxCommitDelay != p+p/5 {
		t.Fatalf("delayed commit: %+v", s)
	}
	// The fence was ready before the targeted vblank (at+p): no late fence.
	if s.lateFences != 0 {
		t.Fatalf("late fence counted: %+v", s)
	}
	// Wanted a little after the flip, committed late: counted likewise,
	// the delay running from when it was wanted.
	at += 2 * p
	flipWanted(o, at+3*p, at+p/4, at+p, at+p+1)
	s = o.flipStats
	if s.missedVblanks != 2 || s.maxInterval != 3*p || s.maxCommitDelay != p+p/5 || s.lateFences != 1 {
		t.Fatalf("wanted after the flip: %+v", s)
	}
}

// A frame wanted long after the previous flip is not due for the next
// vblank, however fast it is committed: not an interval, no delay.
func TestAccountFlipWantedAfterIdleNotCounted(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	flip(o, time.Second, time.Second-p, 0)
	flipWanted(o, 2*time.Second+3*p, 2*time.Second, 2*time.Second+2*p, 0)
	if s := o.flipStats; s != (flipStats{}) || o.lastFlipAt != 2*time.Second+3*p {
		t.Fatalf("idle gap counted: %+v last %s", s, o.lastFlipAt)
	}
}

func TestAccountFlipVRR(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.nominalPeriod()
	o.vrrOn = true
	flip(o, time.Second, time.Second-p, 0)
	// Idle gap under VRR: not an interval.
	flip(o, 2*time.Second, 2*time.Second-time.Millisecond, 2*time.Second-time.Millisecond)
	if s := o.flipStats; s != (flipStats{}) {
		t.Fatalf("idle gap: %+v", s)
	}
	// Back to back: the interval is tracked, but no vblank is missed and
	// no fence is late.
	flip(o, 2*time.Second+3*p, 2*time.Second+p/2, 2*time.Second+3*p-1)
	s := o.flipStats
	if s.missedVblanks != 0 || s.lateFences != 0 || s.maxInterval != 3*p || o.lastFlipAt != 2*time.Second+3*p {
		t.Fatalf("stats %+v", s)
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
	p := o.refreshPeriod()
	flip(o, time.Second, time.Second-p, 0)
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

func TestAccountFlipLateFences(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	at := time.Second
	flip(o, at, at-p, 0)
	// A normal frame: fence signalled during the frame before, flip on
	// the next vblank.
	flip(o, at+p, at+p/2, at+p/4)
	at += p
	flip(o, at+p, at+p/2, at+p-1)
	at += p
	if s := o.flipStats; s.lateFences != 0 || s.missedVblanks != 0 {
		t.Fatalf("normal frame counted: %+v", s)
	}
	// Missed vblank, unknown fence time: not a late fence.
	flip(o, at+2*p, at+p/2, 0)
	at += 2 * p
	// Missed vblank, fence signalled before the targeted vblank (at+p):
	// the frame was ready, something else delayed the flip.
	flip(o, at+2*p, at+p/2, at+p)
	at += 2 * p
	if s := o.flipStats; s.lateFences != 0 || s.missedVblanks != 2 {
		t.Fatalf("early fence counted: %+v", s)
	}
	// Missed vblank, fence after the targeted vblank: late.
	flip(o, at+2*p, at+p/2, at+p+1)
	if s := o.flipStats; s.lateFences != 1 || s.missedVblanks != 3 {
		t.Fatalf("stats %+v", s)
	}
}

func TestTakeFlipStatsResets(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	flip(o, time.Second, time.Second-p, 0)
	flip(o, time.Second+3*p, time.Second+p/2, time.Second+2*p)
	o.accountRead(flipAt(time.Second), time.Second+time.Millisecond)
	flipWanted(o, time.Second+4*p, time.Second+3*p, time.Second+3*p+p/2, 0)
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
	var log bytes.Buffer
	o.log = zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &log})
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if !o.completed(flipEvent{crtc: tCrtc, user: (*commits)[0].user, when: 5 * time.Second}, map[ports.WindowID]uint64{}) {
		t.Fatal("not completed")
	}
	if o.lastFlipAt != 5*time.Second || strings.Contains(log.String(), "slow flip") {
		t.Fatalf("last flip %s: %s", o.lastFlipAt, log.String())
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
