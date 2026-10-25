package hints

import (
	"context"
	"errors"
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestChainOrderCoverageAndLazyCapture(t *testing.T) {
	for _, mode := range []string{"covered", "partial", "empty", "error", "partial-error"} {
		t.Run(mode, func(t *testing.T) {
			wins := []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 40, H: 40}}, {ID: 2, Rect: ports.Rect{X: 40, W: 40, H: 40}}}
			req := ports.HintScanRequest{Width: 80, Height: 40, Scale: 1, Windows: wins}
			a, b, c := newMocksource(t), newMocksource(t), newMocksource(t)
			a.EXPECT().needsFrame(mock.Anything).Return(false)
			a.EXPECT().kind().Return(ports.HintSourceATSPI)
			b.EXPECT().needsFrame(mock.Anything).Return(true).Maybe()
			b.EXPECT().kind().Return(ports.HintSourceOCR).Maybe()
			c.EXPECT().needsFrame(mock.Anything).Return(true).Maybe()
			c.EXPECT().kind().Return(ports.HintSourceDetector).Maybe()
			targets := []ports.HintTarget{{Window: 1, Source: ports.HintSourceATSPI}}
			var stageErr error
			if mode == "covered" {
				targets = append(targets, ports.HintTarget{Window: 2, Source: ports.HintSourceATSPI})
			}
			if mode == "empty" || mode == "error" {
				targets = nil
			}
			if mode == "error" || mode == "partial-error" {
				stageErr = errors.New("source failed")
			}
			first := a.EXPECT().scan(mock.Anything, req, wins, (*frame)(nil)).Return(targets, stageErr).Once()
			if mode != "covered" {
				remaining := wins
				if mode == "partial-error" || mode == "partial" {
					remaining = wins[1:]
				}
				var seen *frame
				second := b.EXPECT().scan(mock.Anything, req, remaining, mock.Anything).RunAndReturn(func(_ context.Context, _ ports.HintScanRequest, _ []ports.HintWindow, fr *frame) ([]ports.HintTarget, error) {
					require.NotNil(t, fr)
					seen = fr
					return nil, nil // zero targets leaves every window for stage three
				}).Once()
				second.NotBefore(first)
				third := c.EXPECT().scan(mock.Anything, req, remaining, mock.Anything).RunAndReturn(func(_ context.Context, _ ports.HintScanRequest, _ []ports.HintWindow, fr *frame) ([]ports.HintTarget, error) {
					require.Same(t, seen, fr)
					return []ports.HintTarget{{Window: remaining[0].ID, Source: ports.HintSourceDetector}}, nil
				}).Once()
				third.NotBefore(second)
			}
			w := worker{sources: []source{a, b, c}}
			captures := make(chan ports.CaptureRequest, 2)
			captured := make(chan ports.CaptureDone, 1)
			captured <- ports.CaptureDone{ID: ports.CaptureIDInternal | 1}
			res := w.serve(context.Background(), req, nil, captures, captured, nil, time.Second)
			require.NoError(t, res.Err)
			wantCaptures := 1
			if mode == "covered" {
				wantCaptures = 0
				require.Equal(t, targets, res.Targets)
			}
			require.Len(t, captures, wantCaptures)
			require.Zero(t, w.live)
		})
	}
}

func TestATSPIChainCoverageUsesFilteredTargets(t *testing.T) {
	tr := newMocktargetSource(t)
	wins := []ports.HintWindow{{ID: 1}}
	tr.EXPECT().Targets(mock.Anything, wins, true).Return([]ports.HintTarget{{Window: 1, Kind: ports.HintButton}}, nil).Once()
	s := atspiSource{targets: tr}
	targets, err := s.scan(context.Background(), ports.HintScanRequest{LinksOnly: true}, wins, nil)
	require.NoError(t, err)
	require.Empty(t, targets)
}

func TestDetectorWindowCropPopupUnion(t *testing.T) {
	det := newMockdetector(t)
	win := ports.HintWindow{ID: 7, Rect: ports.Rect{X: 20, Y: 10, W: 40, H: 30}, Popups: []ports.Rect{{X: 70, Y: 30, W: 20, H: 20}}}
	req := ports.HintScanRequest{Width: 240, Height: 160, Scale: 2}
	det.EXPECT().Detect(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, f detFrame) ([]detection, error) {
		require.Equal(t, image.Rect(40, 20, 180, 100), f.crop)
		require.Equal(t, req.Width*4, f.stride)
		require.Equal(t, tileRegions(140, 80), f.regions)
		return []detection{
			{x1: -20, y1: -20, x2: 20, y2: 20, score: .9},  // clamped, then offset
			{x1: -20, y1: -20, x2: 20, y2: 20, score: .8},  // duplicate within unit
			{x1: 100, y1: 40, x2: 160, y2: 100, score: .9}, // popup, clamped to crop
			{x1: 82, y1: 0, x2: 98, y2: 20, score: .9},     // gap between rect and popup
		}, nil
	}).Once()
	s := detectorSource{det: det}
	out, err := s.scan(context.Background(), req, []ports.HintWindow{win}, &frame{stride: req.Width * 4})
	require.NoError(t, err)
	require.ElementsMatch(t, []ports.HintTarget{
		{Window: 7, Source: ports.HintSourceDetector, Kind: ports.HintButton, Score: .9, Rect: ports.Rect{X: 20, Y: 10, W: 10, H: 10}},
		{Window: 7, Source: ports.HintSourceDetector, Kind: ports.HintButton, Score: .9, Rect: ports.Rect{X: 70, Y: 30, W: 20, H: 20}},
	}, out)
}

func TestDetectorDropSkipsRemainingWindows(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errProtocol, errDetector} {
		t.Run(err.Error(), func(t *testing.T) {
			det := newMockdetector(t)
			wins := []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 20}}, {ID: 2, Rect: ports.Rect{X: 20, W: 20, H: 20}}}
			det.EXPECT().Detect(mock.Anything, mock.Anything).Return(nil, err).Once()
			if errors.Is(err, errDetector) {
				det.EXPECT().Detect(mock.Anything, mock.Anything).Return([]detection{{x2: 10, y2: 10, score: 1}}, nil).Once()
			}
			s := detectorSource{det: det}
			out, gotErr := s.scan(context.Background(), ports.HintScanRequest{Width: 40, Height: 20, Scale: 1}, wins, &frame{})
			require.ErrorIs(t, gotErr, err)
			if errors.Is(err, errDetector) {
				require.Len(t, out, 1)
				require.Equal(t, ports.WindowID(2), out[0].Window)
			} else {
				require.Empty(t, out)
			}
		})
	}
}

func TestOnlyListedSourcesConstructed(t *testing.T) {
	tr := newMocktargetSource(t) // no calls, including Close, when unlisted
	det := newMockdetector(t)
	sources := makeSources(Options{Sources: []ports.HintSource{ports.HintSourceOCR}, ATSPI: tr, detector: det})
	require.Len(t, sources, 1)
	require.IsType(t, &ocrSource{}, sources[0])
	require.NoError(t, sources[0].Close())
	sources = makeSources(Options{Sources: []ports.HintSource{ports.HintSourceDetector, ports.HintSourceOCR, ports.HintSourceATSPI}, ATSPI: tr, detector: det})
	require.IsType(t, &detectorSource{}, sources[0])
	require.IsType(t, &ocrSource{}, sources[1])
	require.IsType(t, &atspiSource{}, sources[2])
}

func TestChainCaptureFailureDiscardsEarlierTargets(t *testing.T) {
	a, b := newMocksource(t), newMocksource(t)
	wins := []ports.HintWindow{{ID: 1}, {ID: 2}}
	req := ports.HintScanRequest{Width: 40, Height: 40, Windows: wins}
	a.EXPECT().needsFrame(mock.Anything).Return(false)
	a.EXPECT().kind().Return(ports.HintSourceATSPI)
	a.EXPECT().scan(mock.Anything, req, wins, (*frame)(nil)).Return([]ports.HintTarget{{Window: 1}}, nil).Once()
	b.EXPECT().needsFrame(mock.Anything).Return(true)
	w := worker{sources: []source{a, b}}
	captures := make(chan ports.CaptureRequest, 1)
	captured := make(chan ports.CaptureDone, 1)
	boom := errors.New("capture failed")
	captured <- ports.CaptureDone{ID: ports.CaptureIDInternal | 1, Err: boom}
	res := w.serve(context.Background(), req, nil, captures, captured, nil, time.Second)
	require.ErrorIs(t, res.Err, boom)
	require.Empty(t, res.Targets)
	require.Zero(t, w.live)
}

func TestOffOutputATSPITargetDoesNotCoverWindow(t *testing.T) {
	for _, scale := range []float64{1, 2} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			wins := []ports.HintWindow{{ID: 1, Rect: ports.Rect{X: -100, W: 150, H: 80}}}
			req := ports.HintScanRequest{Width: 100, Height: 80, Scale: scale, Windows: wins}
			tr := newMocktargetSource(t)
			tr.EXPECT().Targets(mock.Anything, wins, false).Return([]ports.HintTarget{{Window: 1, Source: ports.HintSourceATSPI, Rect: ports.Rect{X: -90, Y: 10, W: 20, H: 20}}, {Window: 1, Source: ports.HintSourceATSPI, Rect: ports.Rect{X: 100, Y: 10, W: 20, H: 20}}}, nil).Once()
			fallback := newMocksource(t)
			fallback.EXPECT().needsFrame(mock.Anything).Return(false)
			fallback.EXPECT().kind().Return(ports.HintSourceOCR)
			want := []ports.HintTarget{{Window: 1, Source: ports.HintSourceOCR}}
			fallback.EXPECT().scan(mock.Anything, req, wins, (*frame)(nil)).Return(want, nil).Once()
			w := worker{sources: []source{&atspiSource{targets: tr}, fallback}}
			res := w.serve(context.Background(), req, nil, nil, nil, nil, time.Second)
			require.NoError(t, res.Err)
			require.Equal(t, want, res.Targets)
		})
	}
}

func TestATSPITargetClippedToLogicalOutput(t *testing.T) {
	tr := newMocktargetSource(t)
	req := ports.HintScanRequest{Width: 100, Height: 80, Scale: 2}
	tr.EXPECT().Targets(mock.Anything, []ports.HintWindow(nil), false).Return([]ports.HintTarget{{Rect: ports.Rect{X: -5, Y: 30, W: 70, H: 20}}}, nil)
	s := atspiSource{targets: tr}
	targets, err := s.scan(context.Background(), req, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, ports.Rect{X: 0, Y: 30, W: 50, H: 10}, targets[0].Rect)
}

func TestChainUnavailableOCRDoesNotCapture(t *testing.T) {
	w := &worker{sources: []source{&ocrSource{state: ocrState{done: true, err: errOCRUnavailable}}}}
	req := ports.HintScanRequest{Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	for range 2 {
		targets, err := w.chain(context.Background(), req, func() (*frame, error) { t.Fatal("unavailable OCR captured a frame"); return nil, nil })
		require.NoError(t, err)
		require.Empty(t, targets)
	}
}

func TestDetectorLinksOnlyDoesNotCapture(t *testing.T) {
	det := newMockdetector(t)
	w := worker{sources: []source{&detectorSource{det: det}}}
	targets, err := w.chain(context.Background(), ports.HintScanRequest{LinksOnly: true, Windows: []ports.HintWindow{{ID: 1}}}, nil)
	require.NoError(t, err)
	require.Empty(t, targets)
}
