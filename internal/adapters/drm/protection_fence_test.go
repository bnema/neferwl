package drm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestProtectedModesetWaitsForClearFence(t *testing.T) {
	o, k, commits, mu := testOutputMu(t)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	r := portsmocks.NewMockRenderer(t)
	f, w := protectionFence(t, false)
	rendered := make(chan struct{})
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		close(rendered)
		return f, nil
	}).Once()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.protectedModeset(ctx, r) }()
	<-rendered
	// The fence remains unreadable; activation must not be queued behind it.
	select {
	case err := <-done:
		t.Fatalf("returned before clear fence signalled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	mu.Lock()
	n := len(*commits)
	mu.Unlock()
	if n != 1 {
		t.Errorf("committed activation before fence signalled: %d commits", n)
	}
	if _, err := w.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 2 || o.back != 1 || o.off {
		t.Fatal("signalled clear did not activate")
	}
}

func TestProtectedModesetPreservesUncommittedGPUReads(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "clear-failure"}[fail], func(t *testing.T) {
			o, k, _ := testOutput(t)
			clientFence, _ := protectionFence(t, false)
			o.holdRead(clientFence)
			owned := o.readFences[0]
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			clearFence, _ := protectionFence(t, true)
			var renderErr error
			if fail {
				renderErr = errors.New("clear failed")
			} else {
				k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
			}
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(clearFence, renderErr).Once()
			err := o.protectedModeset(context.Background(), r)
			if (err != nil) != fail {
				t.Fatalf("clear result: %v", err)
			}
			if o.readDone() || len(o.readFences) != 1 || o.readFences[0] != owned {
				t.Fatal("KMS detachment released an uncommitted GPU read")
			}
			if _, err := owned.Stat(); err != nil {
				t.Fatalf("owned client read fence closed: %v", err)
			}
			o.dropRead() // test cleanup only
		})
	}
}

func TestProtectedCloseCommitFailureDoesNotRestoreDesktop(t *testing.T) {
	o, k, commits := testOutput(t, errors.New("no DRM master"))
	o.protected = true
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	if len(*commits) != 1 || !o.protected {
		t.Fatal("failed close dropped protection or retried restore")
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor)
}
