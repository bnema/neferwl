package wayland

import (
	"bytes"
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

func TestSyncWaitAlreadySignalled(t *testing.T) {
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().signalled(uint32(7), uint64(4)).Return(true, nil).Once()
	sw, err := newSyncWaiter(func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer sw.discard()
	defer sw.close()
	w, err := sw.watch(syncPoint{tl: &timeline{dev: dev, handle: 7}, point: 4})
	if err != nil || !sw.fired(w) {
		t.Fatalf("wait: %v, ready: %v", err, sw.fired(w))
	}
}

// An invalid poll result is an error, never permission to publish.
func TestImplicitPollErrorRejectedAndWarnOnce(t *testing.T) {
	sw, err := newSyncWaiter(func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer sw.discard()
	defer sw.close()
	var output bytes.Buffer
	sw.log = zerowrap.New(zerowrap.Config{Output: &output})
	// An fd number no process can own polls as POLLNVAL; its finalizer
	// cannot close a reused descriptor.
	b := &ports.DMABuf{Planes: []ports.DMABufPlane{{File: os.NewFile(uintptr(math.MaxInt32), "invalid")}}}
	var waits [4]*syncWait
	for range 2 {
		if err := sw.watchImplicit(b, &waits); err == nil || waits[0] != nil {
			t.Fatalf("poll error should reject without registering wait: %v %+v", err, waits)
		}
	}
	if n := bytes.Count(output.Bytes(), []byte("implicit fence poll error")); n != 1 {
		t.Fatalf("poll warnings %d: %s", n, output.String())
	}
}

// waitSyncWake joins the poller's notification after a real pipe becomes ready.
//
//testpolicy:realtime real pipe readiness is observed by the kernel poll waiter
//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
func waitSyncWake(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
	case <-waylandDeadline(time.Second):
		t.Fatal("poll waiter did not notify the owner")
	}
}

// POLLHUP with no POLLIN on a watched fd must wake the surface too.
func TestImplicitPollHangupWakes(t *testing.T) {
	wake := make(chan struct{}, 1)
	sw, err := newSyncWaiter(func() { wake <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.run(ctx); close(done) }()
	defer func() { cancel(); sw.close(); <-done }()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b := &ports.DMABuf{Planes: []ports.DMABufPlane{{File: r}}}
	var waits [4]*syncWait
	if err := sw.watchImplicit(b, &waits); err != nil || waits[0] == nil {
		t.Fatalf("watch: %v", err)
	}
	wr.Close()
	waitSyncWake(t, wake)
	if !sw.fired(waits[0]) || !waits[0].failed {
		t.Fatal("hangup must wake waiter as failed, not ready to publish")
	}
}

func TestImplicitWaitAndDrop(t *testing.T) {
	wake := make(chan struct{}, 1)
	sw, err := newSyncWaiter(func() { wake <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.run(ctx); close(done) }()
	defer func() { cancel(); sw.close(); <-done }()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer wr.Close()
	b := &ports.DMABuf{Planes: []ports.DMABufPlane{{File: r}}}
	var waits [4]*syncWait
	err = sw.watchImplicit(b, &waits)
	if err != nil || waits[0] == nil || sw.fired(waits[0]) {
		t.Fatalf("not ready: %v", err)
	}
	sw.cancel(waits[0])
	waits = [4]*syncWait{}
	err = sw.watchImplicit(b, &waits)
	if err != nil || waits[0] == nil || sw.fired(waits[0]) {
		t.Fatalf("not ready: %v", err)
	}
	if _, err := wr.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	waitSyncWake(t, wake)
	if !sw.fired(waits[0]) {
		t.Fatal("readable buffer never became ready")
	}
	// A newly attached readable buffer is ready without a poll goroutine hop.
	waits = [4]*syncWait{}
	err = sw.watchImplicit(b, &waits)
	if err != nil || waits[0] != nil {
		t.Fatalf("ready: %v, waits: %+v", err, waits)
	}
}
