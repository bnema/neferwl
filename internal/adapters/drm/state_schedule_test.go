package drm

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestStateDelay(t *testing.T) {
	o, _, _ := testOutput(t) // 60 Hz
	p := o.nominalPeriod()
	lead := stateLead(p)
	last := 10 * time.Second
	for _, tc := range []struct {
		name string
		vrr  bool
		last time.Duration
		now  time.Duration
		want time.Duration
	}{
		{"no vblank yet", false, 0, last, 0},
		{"right after a vblank", false, last, last + time.Millisecond, p - lead - time.Millisecond},
		{"inside the lead", false, last, last + p - lead/2, 0},
		{"periods later", false, last, last + 3*p + time.Millisecond, p - lead - time.Millisecond},
		{"stale vblank", false, last, last + vblankStale + time.Millisecond, 0},
		{"VRR", true, last, last + time.Millisecond, 0},
	} {
		o.vrrOn, o.lastVblankAt = tc.vrr, tc.last
		if got := o.stateDelay(tc.now); got != tc.want {
			t.Errorf("%s: delay %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A cursor move right after a vblank waits: the frame that comes before
// the deadline carries it, in a single commit.
func TestDeferredCursorRidesOnFrame(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.stateAt = time.Now().Add(time.Hour) // the vblank is far
	o.cursor.Move(7, 7)
	if err := o.commitState(false); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 0 || o.stateAt.IsZero() || o.frame.pendingCommit() {
		t.Fatalf("cursor committed at once: %d commits, due %v", len(*commits), o.stateAt)
	}
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := (*commits)[0].req.value(tCursor, 14); v != 7 || !o.stateAt.IsZero() {
		t.Fatalf("frame cursor x %d, due %v", v, o.stateAt)
	}
}

// With no frame, the deferred move commits alone once due, and a late
// timer does not push it by another period.
func TestDeferredCursorCommitsWhenDue(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.stateAt = time.Now().Add(time.Hour)
	o.cursor.Move(4, 4)
	if err := o.commitState(false); err != nil || len(*commits) != 0 {
		t.Fatalf("err %v, %d commits", err, len(*commits))
	}
	o.stateAt = time.Now().Add(-time.Millisecond) // the timer fired late
	if err := o.commitState(false); err != nil || len(*commits) != 1 {
		t.Fatalf("due commit: %v, %d commits", err, len(*commits))
	}
	if v, _ := (*commits)[0].req.value(tCursor, 14); v != 4 || !o.stateAt.IsZero() {
		t.Fatalf("cursor x %d, due %v", v, o.stateAt)
	}
}

// A move undone before its deadline commits nothing and clears the wait.
func TestDeferredCursorMovedBack(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(0, 0)
	o.cursor.applied = o.cursor.desired()
	o.stateAt = time.Now().Add(time.Hour)
	o.cursor.Move(9, 9)
	if err := o.commitState(false); err != nil || o.stateAt.IsZero() {
		t.Fatalf("err %v, due %v", err, o.stateAt)
	}
	o.cursor.Move(0, 0)
	if err := o.commitState(false); err != nil || !o.stateAt.IsZero() || len(*commits) != 0 {
		t.Fatalf("err %v, due %v, %d commits", err, o.stateAt, len(*commits))
	}
}

// Powering off drops a deferred state change: an off output has no vblank,
// and an armed deadline would wake Run forever.
func TestPowerOffDropsDeferredState(t *testing.T) {
	o, _, _ := testOutput(t)
	o.stateAt, o.lastVblankAt = time.Now().Add(time.Hour), time.Second
	if err := o.powerOff(); err != nil {
		t.Fatal(err)
	}
	if !o.stateAt.IsZero() || o.lastVblankAt != 0 {
		t.Fatalf("due %v, vblank %v after power off", o.stateAt, o.lastVblankAt)
	}
}

// Vsynced completions, state ones included, give the vblank; a tearing
// flip does not.
func TestCompletionRecordsVblank(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(2, 2)
	if err := o.commitState(false); err != nil || len(*commits) != 1 {
		t.Fatalf("err %v, %d commits", err, len(*commits))
	}
	ev := eventOf((*commits)[0])
	ev.when = 5 * time.Second
	if !o.completed(ev, map[ports.WindowID]uint64{}) || o.lastVblankAt != ev.when {
		t.Fatalf("vblank %v, want %v", o.lastVblankAt, ev.when)
	}
	o.noteVblank(pendingFrame{async: true}, 6*time.Second)
	if o.lastVblankAt != ev.when {
		t.Fatalf("tearing flip taken as a vblank: %v", o.lastVblankAt)
	}
}
