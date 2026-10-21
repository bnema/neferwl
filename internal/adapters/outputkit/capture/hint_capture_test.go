package capture

import (
	"context"
	"image"
	"os"
	"strings"
	"sync"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const internalID = ports.CaptureIDInternal | 1

// hintFrame describes a scene for hint.capture tests: what it draws and
// whether its exclusion is active.
type hintFrame struct {
	capture   bool // hint.capture
	labels    bool // hint mode is shown on this output
	indicator bool
	posHint   bool
	exclusion bool // the scene carries an exclusion session (7) hiding drawn layers
	unshown   bool // ... hiding a surface the scene does not draw
}

func (f hintFrame) scene() ports.Scene {
	s := excludedTestScene()
	s.Capture = nil
	if f.exclusion {
		s.Capture = &ports.SceneCapture{Session: 7, Excluded: []ports.WindowID{10, 20}}
	}
	if f.unshown {
		s.Capture = &ports.SceneCapture{Session: 7, Excluded: []ports.WindowID{99}}
	}
	s.HintStyle.Capture = f.capture
	if f.labels {
		s.HintLabels = []ports.HintLabel{{Text: "f"}}
	}
	if f.indicator {
		s.CaptureIndicators = []ports.CaptureIndicator{{Rect: ports.Rect{W: 10, H: 10}}}
	}
	if f.posHint {
		s.PositionHint = ports.PositionHint{Opacity: 1, Columns: 1}
	}
	return s
}

// drawn names what a rendered scene shows: its labels, indicator and
// position hint, and "excluded" when the excluded layers are gone.
func drawn(s ports.Scene) string {
	var parts []string
	if len(s.HintLabels) > 0 {
		parts = append(parts, "labels")
	}
	if len(s.CaptureIndicators) > 0 {
		parts = append(parts, "indicator")
	}
	if s.PositionHint.Shown() {
		parts = append(parts, "poshint")
	}
	if len(s.Layers) == 2 {
		parts = append(parts, "excluded")
	}
	if len(parts) == 0 {
		return "bare"
	}
	return strings.Join(parts, "+")
}

// frameRun is what one output frame did, as the renderer saw it.
type frameRun struct {
	renders []string         // drawn() of every Render, in call order
	batches []int            // requests copied by each BeginCapture, in call order
	results map[uint64]error // reply of every request
}

// runFrame serves the requests the way the outputs do: Split, SubmitPlain,
// the excluded frame, then the displayed frame and what still reads it.
func runFrame(t *testing.T, s ports.Scene, reqs ...ports.CaptureRequest) frameRun {
	t.Helper()
	replies := make(chan ports.CaptureDone, len(reqs))
	p := NewPipeline(context.Background(), replies)
	r := portsmocks.NewMockRenderer(t)
	var mu sync.Mutex
	var run frameRun
	var frames []*portsmocks.MockCaptureFrame
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		mu.Lock()
		defer mu.Unlock()
		run.renders = append(run.renders, drawn(s))
		return nil, nil
	}).Maybe()
	r.EXPECT().BeginCapture().RunAndReturn(func() (ports.CaptureFrame, error) {
		mu.Lock()
		defer mu.Unlock()
		n := len(run.batches)
		run.batches = append(run.batches, 0)
		f := portsmocks.NewMockCaptureFrame(t)
		f.EXPECT().Done().Return((*os.File)(nil)).Maybe()
		f.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).RunAndReturn(func(image.Rectangle, []byte, int) error {
			mu.Lock()
			defer mu.Unlock()
			run.batches[n]++
			return nil
		}).Maybe()
		frames = append(frames, f)
		return f, nil
	}).Maybe()
	r.EXPECT().EndCapture(mock.Anything).Return().Maybe()

	normal, excluded, hidden := p.Split(s, reqs)
	require.Empty(t, hidden)
	took, done, err := p.SubmitPlain(r, s, nil, normal)
	require.NoError(t, err)
	require.Nil(t, done)
	if took {
		normal = nil
	}
	if len(excluded) > 0 {
		_, _ = r.Render(p.ExcludedScene(s), nil)
		p.SubmitScoped(s.Security, r, excluded)
	}
	_, _ = r.Render(s, nil)
	p.SubmitScoped(s.Security, r, normal)

	run.results = map[uint64]error{}
	for range reqs {
		d := awaitCapture(t, replies)
		run.results[d.ID] = d.Err
	}
	p.Close(r)
	for _, f := range frames {
		f.Calls = nil // Read's arguments point into unmapped destinations
	}
	return run
}

func client(t *testing.T, id uint64) ports.CaptureRequest { return pipelineRequest(t, id) }

func clientExclude(t *testing.T, id uint64) ports.CaptureRequest {
	q := pipelineRequest(t, id)
	q.Exclude, q.Session = true, 7
	return q
}

func internalReq(t *testing.T, exclude bool) ports.CaptureRequest {
	q := pipelineRequest(t, internalID)
	if exclude {
		q.Exclude, q.Session = true, 7
	}
	return q
}

// Every frame shape hint.capture can produce: what is rendered, how many
// captures each batch copies, which requests fail. The displayed frame is
// always the last render. No frame needs more than two batches.
func TestHintCaptureFrames(t *testing.T) {
	const (
		displayed = "labels"
		bare      = "bare"
	)
	tests := []struct {
		name    string
		frame   hintFrame
		reqs    func(t *testing.T) []ports.CaptureRequest
		renders []string
		batches []int
		failed  map[uint64]error // want errors (ErrorIs); every other request succeeds
	}{
		{
			name:  "off: labels leave every capture, one plain frame serves both",
			frame: hintFrame{labels: true},
			reqs: func(t *testing.T) []ports.CaptureRequest {
				return []ports.CaptureRequest{client(t, 1), internalReq(t, false)}
			},
			renders: []string{bare, displayed},
			batches: []int{2},
		},
		{
			name:    "on: a client capture reads the displayed frame, labels included",
			frame:   hintFrame{capture: true, labels: true},
			reqs:    func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{client(t, 1)} },
			renders: []string{displayed},
			batches: []int{1},
		},
		{
			name:    "on: the indicator is dropped, the labels are kept",
			frame:   hintFrame{capture: true, labels: true, indicator: true},
			reqs:    func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{client(t, 1)} },
			renders: []string{"labels", "labels+indicator"},
			batches: []int{1},
		},
		{
			name:    "on: the position hint is dropped, the labels are kept",
			frame:   hintFrame{capture: true, labels: true, posHint: true},
			reqs:    func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{client(t, 1)} },
			renders: []string{"labels", "labels+poshint"},
			batches: []int{1},
		},
		{
			name:  "on: an internal capture of a labelled frame fails, the client is served",
			frame: hintFrame{capture: true, labels: true, indicator: true},
			reqs: func(t *testing.T) []ports.CaptureRequest {
				return []ports.CaptureRequest{client(t, 1), internalReq(t, false)}
			},
			renders: []string{"labels", "labels+indicator"},
			batches: []int{1},
			failed:  map[uint64]error{internalID: ErrInternalLabelled},
		},
		{
			name:  "on: an internal capture alone fails and renders nothing for it",
			frame: hintFrame{capture: true, labels: true},
			reqs:  func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{internalReq(t, false)} },
			// the displayed frame is still drawn by the output, with no batch
			renders: []string{displayed},
			failed:  map[uint64]error{internalID: ErrInternalLabelled},
		},
		{
			name:  "on: an internal Exclude capture fails, its excluded frame has labels",
			frame: hintFrame{capture: true, labels: true, exclusion: true},
			reqs:  func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{internalReq(t, true)} },
			// no excluded frame is rendered for a failed request
			renders: []string{displayed},
			failed:  map[uint64]error{internalID: ErrInternalExclude},
		},
		{
			name:  "on: client, internal and Exclude requests over an indicator fit two batches",
			frame: hintFrame{capture: true, labels: true, indicator: true, posHint: true, exclusion: true},
			reqs: func(t *testing.T) []ports.CaptureRequest {
				return []ports.CaptureRequest{client(t, 1), internalReq(t, false), clientExclude(t, 3)}
			},
			// plain frame for the client, excluded frame (labels kept, no
			// indicator, no position hint) for Exclude, then the displayed one
			renders: []string{"labels", "labels+excluded", "labels+indicator+poshint"},
			batches: []int{1, 1},
			failed:  map[uint64]error{internalID: ErrInternalLabelled},
		},
		{
			name:    "on: labels alone do not force the excluded frame",
			frame:   hintFrame{capture: true, labels: true, unshown: true},
			reqs:    func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{clientExclude(t, 1)} },
			renders: []string{displayed},
			batches: []int{1},
		},
		{
			name:    "off: labels force the excluded frame, without labels",
			frame:   hintFrame{labels: true, exclusion: true},
			reqs:    func(t *testing.T) []ports.CaptureRequest { return []ports.CaptureRequest{clientExclude(t, 1)} },
			renders: []string{"excluded", displayed},
			batches: []int{1},
		},
		{
			name:  "on without labels shown: internal and client share the plain frame",
			frame: hintFrame{capture: true, indicator: true},
			reqs: func(t *testing.T) []ports.CaptureRequest {
				return []ports.CaptureRequest{client(t, 1), internalReq(t, false)}
			},
			renders: []string{bare, "indicator"},
			batches: []int{2},
		},
		{
			name:  "nothing to hide: the displayed frame serves everyone",
			frame: hintFrame{capture: true},
			reqs: func(t *testing.T) []ports.CaptureRequest {
				return []ports.CaptureRequest{client(t, 1), internalReq(t, false)}
			},
			renders: []string{bare},
			batches: []int{2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runFrame(t, tt.frame.scene(), tt.reqs(t)...)
			require.Equal(t, tt.renders, run.renders)
			require.Equal(t, tt.batches, run.batches)
			require.LessOrEqual(t, len(run.batches), pipelineSlots)
			for id, err := range run.results {
				if want, ok := tt.failed[id]; ok {
					require.ErrorIs(t, err, want, "request %#x", id)
					require.ErrorIs(t, err, ports.ErrCaptureTransient)
				} else {
					require.NoError(t, err, "request %#x", id)
				}
			}
		})
	}
}

// Splitting and building the excluded scene of a frame that keeps labels
// allocates nothing.
func TestSplitNoAllocHintCapture(t *testing.T) {
	p := NewPipeline(context.Background(), make(chan ports.CaptureDone, 8))
	defer p.Close(nil)
	s := hintFrame{capture: true, labels: true, indicator: true, exclusion: true}.scene()
	reqs := make([]ports.CaptureRequest, 3)
	if allocs := testing.AllocsPerRun(100, func() {
		reqs[0] = ports.CaptureRequest{ID: 1}
		reqs[1] = ports.CaptureRequest{ID: 2, Exclude: true, Session: 7}
		reqs[2] = ports.CaptureRequest{ID: 3, Exclude: true, Session: 7}
		_ = p.ExcludedScene(s)
		p.Split(s, reqs)
	}); allocs != 0 {
		t.Fatalf("%.1f allocs, want 0", allocs)
	}
}
