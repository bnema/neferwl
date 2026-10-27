package drm

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// A static scene produces no reportDirty wake. Memory retries must reap
// completed reads themselves, without dropping an older unsignalled read.
func TestNoMemRetriesReapSignalledReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		errs := make([]error, 40)
		for i := range errs {
			errs[i] = unix.ENOMEM
		}
		o, _, commits := testOutput(t, errs...)
		defer o.dropRead()
		held, signal := fencePipe(t)
		o.holdRead(held)
		pending := o.readFences[0]
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().UseTarget(0).Return()
		r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(domain.Scene, map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
			f, w := fencePipe(t)
			if _, err := w.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			return f, nil
		}).Times(len(errs))
		enabled := true
		var previous *os.File
		for range errs {
			_, err := o.submitFrame(context.Background(), r, domain.Scene{Seq: 1}, nil, nil, nil, nil)
			if !errors.Is(err, unix.ENOMEM) || !o.commitFailed(err, &enabled) {
				t.Fatalf("refusal: %v", err)
			}
			if len(o.readFences) != 2 || o.readFences[0] != pending {
				t.Fatalf("retained %d fences; unsignalled read preserved=%v", len(o.readFences), len(o.readFences) > 0 && o.readFences[0] == pending)
			}
			if previous != nil {
				if _, err := previous.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("completed descriptor not closed: %v", err)
				}
			}
			previous = o.readFences[1]
			time.Sleep(o.frame.nomemBackoff)
			if o.expire() {
				t.Fatal("memory retry requested modeset")
			}
		}
		if len(*commits) != len(errs) {
			t.Fatalf("commits %d", len(*commits))
		}
		if o.readDone() || len(o.readFences) != 1 || o.readFences[0] != pending {
			t.Fatal("unsignalled read lost")
		}
		if _, err := signal.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		if !o.readDone() || len(o.readFences) != 0 {
			t.Fatal("completed read not reaped")
		}
	})
}
