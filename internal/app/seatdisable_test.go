package app

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
)

// Every running output is asked, in parallel, with the bound; an output
// that has stopped is not.
func TestPrepareSeatDisableAsksRunningOutputs(t *testing.T) {
	b := &drmBackend{log: zerowrap.Default()}
	a, c, gone := newMockseatDisablePreparer(t), newMockseatDisablePreparer(t), newMockseatDisablePreparer(t)
	var asked atomic.Int32
	for _, p := range []*mockseatDisablePreparer{a, c} {
		p.EXPECT().PrepareSeatDisable(seatDisableTimeout).RunAndReturn(func(time.Duration) bool { asked.Add(1); return true }).Once()
	}
	b.addPreparer(a)
	b.addPreparer(c)
	b.addPreparer(gone)
	b.removePreparer(gone)
	b.prepareSeatDisable()
	if asked.Load() != 2 {
		t.Fatalf("%d outputs asked", asked.Load())
	}
}

// A late output is reported once and the disable goes on; outputs that
// answer in time, or none at all, log nothing.
func TestPrepareSeatDisableWarnsOnlyWhenLate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		acks  []bool
		warns bool
	}{
		{"no outputs", nil, false},
		{"in time", []bool{true, true}, false},
		{"one late", []bool{true, false}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &lockedBuffer{}
			b := &drmBackend{log: zerowrap.New(zerowrap.Config{Format: "json", Output: log})}
			for _, ack := range tc.acks {
				p := newMockseatDisablePreparer(t)
				p.EXPECT().PrepareSeatDisable(mock.Anything).Return(ack).Once()
				b.addPreparer(p)
			}
			b.prepareSeatDisable()
			if got := strings.Contains(log.String(), `"outputs":1`); got != tc.warns {
				t.Fatalf("late warning %v, want %v: %s", got, tc.warns, log.String())
			}
		})
	}
}
