package drm

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestMasterRetryBackoff(t *testing.T) {
	var m masterRetry
	enabled := false
	if _, ok := m.next(); ok {
		t.Fatal("retry due before a refusal")
	}
	m.refused(unix.EBUSY, &enabled)
	if _, ok := m.next(); ok {
		t.Fatal("a busy refusal is retried by the busy path, not here")
	}
	m.refused(unix.EACCES, &enabled)
	d, ok := m.next()
	if !ok || d != masterRetryFirst {
		t.Fatalf("first retry %v %v", d, ok)
	}
	m.attempts = 30
	if d, _ := m.next(); d != masterRetryMax {
		t.Fatalf("capped retry %v", d)
	}
	m.attempts = masterRetryLimit
	if _, ok := m.next(); ok {
		t.Fatal("retries past the limit")
	}
	m.done()
	if _, ok := m.next(); ok {
		t.Fatal("retry after done")
	}
}

// The seat reports active before the kernel gives DRM master back: the
// enable modeset is refused, then retried until it succeeds, without a
// second seat change.
func TestEnableRefusedForMasterIsRetried(t *testing.T) {
	synctest.Test(t, enableRefusedForMasterIsRetried)
}

func enableRefusedForMasterIsRetried(t *testing.T) {
	refuse := true
	o, k, commits, commitMu := testOutputRules(t, func(_ *atomicReq, flags uint32) error {
		if refuse && flags&atomicAllowModes != 0 && flags&atomicTestOnly == 0 {
			return unix.EACCES
		}
		return nil
	})
	o.cursor, o.tearing = nil, false
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(mock.Anything).Return().Maybe()
	r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	active := make(chan bool, 1)
	active <- false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, make(chan ports.OutputPresented, 1), nil, nil)
	}()
	modesets := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		n := 0
		for _, c := range *commits {
			if c.flags&atomicAllowModes != 0 && c.flags&atomicTestOnly == 0 {
				n++
			}
		}
		return n
	}
	synctest.Wait()
	active <- true
	synctest.Wait()
	if n := modesets(); n != 1 {
		t.Fatalf("%d modesets after the refused enable", n)
	}
	time.Sleep(masterRetryFirst)
	synctest.Wait()
	if n := modesets(); n != 2 {
		t.Fatalf("%d modesets after the first retry", n)
	}
	commitMu.Lock()
	refuse = false
	commitMu.Unlock()
	time.Sleep(2 * masterRetryFirst)
	synctest.Wait()
	if n := modesets(); n != 3 {
		t.Fatalf("%d modesets after the retry that succeeds", n)
	}
	// Once enabled, nothing retries any more.
	time.Sleep(masterRetryMax * 2)
	synctest.Wait()
	if n := modesets(); n != 3 {
		t.Fatalf("%d modesets after success", n)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
