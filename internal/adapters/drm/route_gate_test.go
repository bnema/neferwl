package drm

import (
	"context"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// A scene that needs route generation 2 is not rendered until a batch
// carrying it arrives; without one it renders after routeWait (100 ms).
func TestRunGatesFrameOnRouteGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sr := startStampRun(t)
		win := []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 10, H: 10}}}
		sr.scenes <- domain.Scene{Seq: 1, OutputWidth: 200, OutputHeight: 100, Windows: win}
		waitFor(t, func() bool { return sr.frameCommit() == 1 }) // Routes=0 never gates
		sr.flips <- flipEvent{crtc: tCrtc, user: sr.lastCommit().user, when: time.Second, seq: 1}
		synctest.Wait()

		sr.scenes <- domain.Scene{Seq: 2, Routes: 2, OutputWidth: 200, OutputHeight: 100, Windows: win}
		synctest.Wait()
		time.Sleep(50 * time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		synctest.Wait()
		if n := sr.frameCommit(); n != 1 {
			t.Fatalf("%d frames while the routed contents are missing", n)
		}
		sr.contents.ch <- &domain.ContentBatch{Routes: 2}
		synctest.Wait()
		if n := sr.frameCommit(); n != 2 {
			t.Fatalf("%d frames after the batch with the generation", n)
		}
		sr.flips <- flipEvent{crtc: tCrtc, user: sr.lastCommit().user, when: 2 * time.Second, seq: 2}
		synctest.Wait()

		sr.scenes <- domain.Scene{Seq: 3, Routes: 3, OutputWidth: 200, OutputHeight: 100, Windows: win}
		synctest.Wait()
		time.Sleep(100*time.Millisecond - time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		synctest.Wait()
		if n := sr.frameCommit(); n != 2 {
			t.Fatalf("%d frames before routeWait", n)
		}
		time.Sleep(time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		synctest.Wait()
		if n := sr.frameCommit(); n != 3 {
			t.Fatalf("%d frames after routeWait, want it rendered anyway", n)
		}
		sr.stop()
	})
}

// A cursor commit is a state commit: it is not held by the gate.
func TestRunCursorCommitsWhileRouteGated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o, k, commits, commitMu := testOutputMu(t)
		o.tearing, o.cursor.fbs = false, [2]uint32{}
		flips := make(chan flipEvent, 1)
		o.flipped = flips
		r := portsmocks.NewMockRenderer(t)
		buf := func() domain.DMABuf {
			f, w, _ := os.Pipe()
			_ = w.Close()
			return domain.DMABuf{Planes: []domain.DMABufPlane{{File: f}}}
		}
		r.EXPECT().CursorBuffers(64).Return([2]domain.DMABuf{buf(), buf()}, nil).Once()
		r.EXPECT().WriteCursor(mock.Anything, mock.Anything, 1, 1).Return(nil)
		r.EXPECT().SetHDR(float64(0)).Return().Maybe()
		r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]domain.DMABuf{buf(), buf()}, nil).Once()
		r.EXPECT().UseTarget(mock.Anything).Return()
		r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
		r.EXPECT().Close().Return().Once()
		k.EXPECT().createDumb(64, 64).Return(dumbBuffer{}, unix.ENOSYS).Once()
		k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(90, nil).Once()
		k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(91, nil).Once()
		k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
		k.EXPECT().createBlob(mock.Anything).Return(99, nil)
		k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
		k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
		load := func(domain.CursorChange, float64, domain.BufferTransform, int) (domain.CursorImage, error) {
			return domain.CursorImage{Pixels: []byte{1, 2, 3, 4}, W: 1, H: 1}, nil
		}
		count := func(kind uint64) int {
			commitMu.Lock()
			defer commitMu.Unlock()
			n := 0
			for _, c := range *commits {
				if c.user&3 == kind {
					n++
				}
			}
			return n
		}
		last := func() commitRec {
			commitMu.Lock()
			defer commitMu.Unlock()
			return (*commits)[len(*commits)-1]
		}
		scenes := make(chan domain.Scene, 1)
		contents := newContentFeed(t, 1)
		sink, _ := presentedSink(t, 8)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		setOutputRenderer(t, o, r)
		go func() {
			done <- o.Run(ctx, load, make(chan bool), scenes, contents.in(), contents.ret, nil, sink, nil, nil)
		}()
		scenes <- domain.Scene{Seq: 1, OutputWidth: 200, OutputHeight: 100, Scale: 1}
		synctest.Wait()
		flips <- eventOf(last())
		synctest.Wait()
		frames, states := count(userFrame), count(userState)
		scenes <- domain.Scene{Seq: 2, Routes: 5, OutputWidth: 200, OutputHeight: 100, Scale: 1}
		synctest.Wait()
		o.cursor.Move(3, 3)
		synctest.Wait()
		if count(userFrame) != frames {
			t.Fatal("a gated scene was rendered")
		}
		if count(userState) != states+1 {
			t.Fatalf("cursor state commits %d, want %d: the gate must not hold them", count(userState), states+1)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// A protected (lock) scene draws no client window: it never waits for routed
// contents.
func TestRunDoesNotGateProtectedSceneOnRouteGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sr := startStampRun(t)
		sr.state.Store(3) // generation 1, protected
		sr.wake <- domain.SecurityState{Generation: 1, Protected: true}
		synctest.Wait()
		sr.flips <- flipEvent{crtc: tCrtc, user: sr.lastCommit().user, when: time.Second, seq: 1}
		synctest.Wait()
		frames := sr.frameCommit()
		sr.scenes <- domain.Scene{Seq: 1, Routes: 2, Security: domain.SecurityState{Generation: 1, Protected: true}, OutputWidth: 200, OutputHeight: 100}
		synctest.Wait()
		if n := sr.frameCommit(); n != frames+1 {
			t.Fatalf("%d frames, want %d: a protected scene must not wait for routed contents", n, frames+1)
		}
		sr.stop()
	})
}
