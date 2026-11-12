package headless

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// A scene with Routes=0 renders at once. One that needs generation 2 waits
// for a batch carrying it; with none, the route timer renders it after
// routeWait (100 ms).
func TestRunGatesFrameOnRouteGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk := portsmocks.NewMockClock(t)
		clk.EXPECT().Now().RunAndReturn(time.Now)
		idle := func() *portsmocks.MockTimer {
			tk := portsmocks.NewMockTimer(t)
			tk.EXPECT().C().Return(make(chan time.Time)).Maybe()
			tk.EXPECT().Stop().Return(true).Maybe()
			tk.EXPECT().Reset(mock.Anything).Return(true).Maybe()
			return tk
		}
		clk.EXPECT().NewTimer(trimEvery).Return(idle()).Once()
		clk.EXPECT().NewTimer(time.Hour).Return(idle()).Once()
		routeC := make(chan time.Time, 1)
		route := portsmocks.NewMockTimer(t)
		route.EXPECT().C().Return(routeC).Maybe()
		route.EXPECT().Stop().Return(true).Maybe()
		route.EXPECT().Reset(mock.Anything).Return(true).Maybe()
		clk.EXPECT().NewTimer(100 * time.Millisecond).Return(route).Once()

		r, f := recordingRenderer(t, nil)
		scenes := make(chan domain.Scene)
		contents := newContentFeed(t, 4)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Clock: clk, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		frames := func() int { s, _ := f.snapshot(); return len(s) }
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, f, 1)

		scenes <- domain.Scene{Seq: 2, Routes: 2, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		time.Sleep(50 * time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		synctest.Wait()
		if n := frames(); n != 1 {
			t.Fatalf("%d frames while the routed contents are missing", n)
		}
		contents.ch <- &domain.ContentBatch{Routes: 2}
		synctest.Wait()
		if n := frames(); n != 2 {
			t.Fatalf("%d frames after the batch carrying the generation", n)
		}

		scenes <- domain.Scene{Seq: 3, Routes: 3, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		time.Sleep(99 * time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		synctest.Wait()
		if n := frames(); n != 2 {
			t.Fatalf("%d frames before routeWait", n)
		}
		time.Sleep(time.Millisecond) //testpolicy:sleep fake clock inside the bubble
		routeC <- time.Now()
		synctest.Wait()
		if n := frames(); n != 3 {
			t.Fatalf("%d frames after routeWait, want it rendered anyway", n)
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
		clk := portsmocks.NewMockClock(t)
		clk.EXPECT().Now().RunAndReturn(time.Now)
		idle := func() *portsmocks.MockTimer {
			tk := portsmocks.NewMockTimer(t)
			tk.EXPECT().C().Return(make(chan time.Time)).Maybe()
			tk.EXPECT().Stop().Return(true).Maybe()
			tk.EXPECT().Reset(mock.Anything).Return(true).Maybe()
			return tk
		}
		clk.EXPECT().NewTimer(trimEvery).Return(idle()).Once()
		clk.EXPECT().NewTimer(time.Hour).Return(idle()).Once()
		r, f := recordingRenderer(t, nil)
		scenes := make(chan domain.Scene)
		contents := newContentFeed(t, 4)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Clock: clk, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		scenes <- domain.Scene{Seq: 1, Routes: 2, Security: domain.SecurityState{Generation: 1, Protected: true}, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, f, 1)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
