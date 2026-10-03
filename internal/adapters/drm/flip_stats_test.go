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

func TestAccountFlipMissedVblanks(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	if p < period60-time.Microsecond || p > period60+time.Microsecond {
		t.Fatalf("period %s", p)
	}
	at := time.Second
	// The first flip has no interval.
	o.accountFlip(flipAt(at), true, at, 0)
	if s := o.flipStats; s.missedVblanks != 0 || s.maxInterval != 0 || o.lastFlipAt != at {
		t.Fatalf("first flip: %+v last %s", s, o.lastFlipAt)
	}
	// On time, and just under 1.5 periods: not missed.
	at += p
	o.accountFlip(flipAt(at), true, at, 0)
	at += p * 3 / 2
	o.accountFlip(flipAt(at), true, at, 0)
	if o.flipStats.missedVblanks != 0 {
		t.Fatalf("on time counted: %+v", o.flipStats)
	}
	// Two periods: one missed vblank; the longest interval is kept.
	at += 2 * p
	o.accountFlip(flipAt(at), true, at, 0)
	at += 3 * p
	o.accountFlip(flipAt(at), true, at, 0)
	at += p
	o.accountFlip(flipAt(at), true, at, 0)
	if s := o.flipStats; s.missedVblanks != 2 || s.maxInterval != 3*p {
		t.Fatalf("stats %+v", s)
	}
}

func TestAccountFlipVRRSkipsMissedVblanks(t *testing.T) {
	o, _, _ := testOutput(t)
	o.vrrOn = true
	o.accountFlip(flipAt(time.Second), true, time.Second, 0)
	o.accountFlip(flipAt(2*time.Second), true, 2*time.Second, time.Second+time.Millisecond)
	s := o.flipStats
	if s.missedVblanks != 0 || s.lateFences != 0 || s.maxInterval != time.Second || o.lastFlipAt != 2*time.Second {
		t.Fatalf("stats %+v", s)
	}
}

// A state commit (cursor, VRR) is neither a frame interval nor a frame.
func TestAccountFlipStateCommit(t *testing.T) {
	o, _, _ := testOutput(t)
	o.accountFlip(flipAt(time.Second), true, time.Second, 0)
	o.accountFlip(flipAt(time.Second+10*period60), false, time.Second+10*period60+time.Millisecond, 0)
	s := o.flipStats
	if s.missedVblanks != 0 || s.maxInterval != 0 || o.lastFlipAt != time.Second || s.maxFlipToRead != time.Millisecond {
		t.Fatalf("stats %+v last %s", s, o.lastFlipAt)
	}
}

func TestAccountFlipMaxFlipToRead(t *testing.T) {
	o, _, _ := testOutput(t)
	for _, d := range []time.Duration{2 * time.Millisecond, 25 * time.Millisecond, 5 * time.Millisecond} {
		o.accountFlip(flipAt(time.Second), true, time.Second+d, 0)
	}
	if got := o.flipStats.maxFlipToRead; got != 25*time.Millisecond {
		t.Fatalf("max flip to read %s", got)
	}
}

func TestAccountFlipLateFences(t *testing.T) {
	o, _, _ := testOutput(t)
	p := o.refreshPeriod()
	when := time.Second
	// Unknown fence time (0): never late. Signalled before the previous
	// vblank, or exactly on it: not late.
	o.accountFlip(flipAt(when), true, when, 0)
	o.accountFlip(flipAt(when+p), true, when+p, when-time.Millisecond)
	o.accountFlip(flipAt(when+2*p), true, when+2*p, when+p)
	if o.flipStats.lateFences != 0 {
		t.Fatalf("on time counted: %+v", o.flipStats)
	}
	// Signalled after the vblank before the flip: late.
	o.accountFlip(flipAt(when+3*p), true, when+3*p, when+2*p+time.Microsecond)
	// A state commit never counts.
	o.accountFlip(flipAt(when+4*p), false, when+4*p, when+4*p-time.Microsecond)
	if o.flipStats.lateFences != 1 {
		t.Fatalf("stats %+v", o.flipStats)
	}
}

func TestTakeFlipStatsResets(t *testing.T) {
	o, _, _ := testOutput(t)
	o.accountFlip(flipAt(time.Second), true, time.Second+time.Millisecond, 0)
	o.accountFlip(flipAt(time.Second+3*period60), true, time.Second+3*period60+time.Millisecond, time.Second+3*period60)
	s := o.takeFlipStats()
	if s.missedVblanks != 1 || s.lateFences != 1 || s.maxInterval == 0 || s.maxFlipToRead != time.Millisecond {
		t.Fatalf("taken %+v", s)
	}
	if o.flipStats != (flipStats{}) {
		t.Fatalf("not reset: %+v", o.flipStats)
	}
	// The interval chain survives the report.
	if o.lastFlipAt != time.Second+3*period60 {
		t.Fatalf("last flip %s", o.lastFlipAt)
	}
}

// Every frame flip keeps lastFlipAt, traced or not.
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

func TestAccountFlipAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	at := time.Second
	if n := testing.AllocsPerRun(100, func() {
		at += 40 * time.Millisecond
		o.accountFlip(flipAt(at), true, at+time.Millisecond, at-30*time.Millisecond)
		o.accountFlip(flipAt(at), false, at+time.Millisecond, 0)
		_ = o.takeFlipStats()
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}
