package app

import (
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

// A slow output only costs its own bound: the disable goes on.
func TestPrepareSeatDisableGoesOnAfterTimeout(t *testing.T) {
	b := &drmBackend{log: zerowrap.Default()}
	p := newMockseatDisablePreparer(t)
	p.EXPECT().PrepareSeatDisable(mock.Anything).Return(false).Once()
	b.addPreparer(p)
	b.prepareSeatDisable() // returns
}

func TestPrepareSeatDisableWithoutOutputs(t *testing.T) {
	(&drmBackend{log: zerowrap.Default()}).prepareSeatDisable()
}
