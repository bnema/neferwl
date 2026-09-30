package capture

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"sync/atomic"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSubmitScopedRejectsChangedAndProtectedEpoch(t *testing.T) {
	for _, state := range []ports.SecurityState{{Generation: 4}, {Generation: 3, Protected: true}} {
		t.Run(fmt.Sprintf("generation-%d-protected-%v", state.Generation, state.Protected), func(t *testing.T) {
			replies := make(chan ports.CaptureDone, 2)
			p := NewPipeline(context.Background(), replies)
			security := portsmocks.NewMockSessionSecurity(t)
			security.EXPECT().Snapshot().Return(state)
			p.Security = security
			r := portsmocks.NewMockRenderer(t)
			q := pipelineRequest(t, 1)
			file := q.Dst.File
			p.SubmitScoped(ports.SecurityState{Generation: 3}, r, []ports.CaptureRequest{q})
			p.Close(r)
			require.Len(t, replies, 1)
			require.ErrorIs(t, (<-replies).Err, ErrSecurityState)
			r.AssertNotCalled(t, "BeginCapture")
			_, err := file.Stat()
			require.ErrorIs(t, err, os.ErrClosed)
		})
	}
}

func TestSubmitHiddenEngageDuringRenderRejectsCopyRetainsReads(t *testing.T) {
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	security := portsmocks.NewMockSessionSecurity(t)
	var engaged atomic.Bool
	security.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
		if engaged.Load() {
			return ports.SecurityState{Generation: 1, Protected: true}
		}
		return ports.SecurityState{}
	})
	p.Security = security
	child := portsmocks.NewMockRenderer(t)
	f, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { f.Close(); w.Close() })
	child.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		engaged.Store(true)
		return f, nil
	}).Once()
	child.EXPECT().Close().Return().Once()
	p.EnableOffscreen(func(int, int) (ports.Renderer, error) { return child, nil })
	s := hiddenScene()
	s.CaptureScene.Windows = []ports.SceneWindow{{ID: 5}}
	surfaces := map[ports.WindowID]ports.SurfaceContent{5: {ID: 5, Seq: 8}}
	reqs := []ports.CaptureRequest{hiddenRequest(t, 1, image.Rect(0, 0, 200, 100)), hiddenRequest(t, 2, image.Rect(0, 0, 200, 100))}
	files := []*os.File{reqs[0].Dst.File, reqs[1].Dst.File}
	p.SubmitHidden(s, surfaces, reqs)
	child.AssertNotCalled(t, "BeginCapture")
	require.True(t, p.HiddenReading(), "rejected native capture still owns GPU reads")
	seen, reads, limited := p.CapHiddenSeen(map[ports.WindowID]uint64{5: 9})
	require.True(t, limited)
	require.Equal(t, uint64(8), seen[5])
	require.Equal(t, uint64(8), reads[5])
	for range 2 {
		reply := awaitCapture(t, replies)
		require.ErrorIs(t, reply.Err, ErrSecurityState)
	}
	for _, file := range files {
		_, err := file.Stat()
		require.ErrorIs(t, err, os.ErrClosed)
	}
	require.Empty(t, replies, "exactly one failure reply for each request credit")
	_, err = f.Stat()
	require.NoError(t, err, "render fence stays owned until signalling")
	_, err = w.Write([]byte{1})
	require.NoError(t, err)
	require.False(t, p.HiddenReading())
	_, err = f.Stat()
	require.True(t, errors.Is(err, os.ErrClosed), "signalled child fence closes")
	p.Close(nil)
	require.Empty(t, replies, "closing rejected pipeline creates no duplicate reply")
}
