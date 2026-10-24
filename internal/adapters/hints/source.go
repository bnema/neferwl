package hints

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// Options tunes Run.
type Options struct {
	// Sources is the startup-only ordered chain. Only listed sources exist.
	Sources []ports.HintSource
	// ATSPI supplies the accessibility source when listed.
	ATSPI targetSource
	// Command is the nefer-hints binary: a path, or a name looked up in PATH.
	Command string
	// ModelDir is the directory of the detector's model files.
	ModelDir string
	// Threshold overrides the minimum detection score, 0.01 to 0.99.
	// Zero uses model.json or, if absent, the runner default.
	Threshold float64
	// DumpDir, when not empty, receives the scanned frames (PNG, annotated
	// PNG and JSON). They contain the screen in clear.
	DumpDir string

	// detector replaces the subprocess in tests.
	detector detector
}

// source is owned by the worker. Initialization is deferred to its first scan.
// Returned targets have Source and Window set; errors preserve partial targets.
type source interface {
	kind() ports.HintSource
	needsFrame() bool
	scan(context.Context, ports.HintScanRequest, []ports.HintWindow, *frame) ([]ports.HintTarget, error)
	Close() error
}

func makeSources(opts Options) []source {
	var out []source
	for _, kind := range opts.Sources {
		switch kind {
		case ports.HintSourceATSPI:
			out = append(out, &atspiSource{targets: opts.ATSPI})
		case ports.HintSourceOCR:
			out = append(out, &ocrSource{})
		case ports.HintSourceDetector:
			det := opts.detector
			if det == nil {
				det = newProcDetector(opts.Command, opts.ModelDir, opts.Threshold)
			}
			out = append(out, &detectorSource{det: det})
		}
	}
	return out
}

// chain covers a unit only after a stage returns at least one target in it.
func (w *worker) chain(ctx context.Context, req ports.HintScanRequest, capture func() (*frame, error)) ([]ports.HintTarget, error) {
	covered := make(map[ports.WindowID]bool)
	var out []ports.HintTarget
	var fr *frame
	var err error
	if w.dumpDir != "" {
		fr, err = capture()
		if err != nil {
			return nil, err
		}
	}
	for _, s := range w.sources {
		var wins []ports.HintWindow
		for _, win := range req.Windows {
			if !covered[win.ID] {
				wins = append(wins, win)
			}
		}
		if len(wins) == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.needsFrame() && fr == nil {
			fr, err = capture()
			if err != nil {
				return nil, err
			}
		}
		start := time.Now()
		input := fr
		if !s.needsFrame() {
			input = nil
		}
		targets, err := s.scan(ctx, req, wins, input)
		log := logging.For(ctx, "hints")
		if err != nil {
			if w.warned == nil {
				w.warned = make(map[ports.HintSource]bool)
			}
			if !w.warned[s.kind()] {
				log.Warn().Err(err).Str("source", s.kind().String()).Msg("hint source failed")
				w.warned[s.kind()] = true
			} else {
				log.Debug().Err(err).Str("source", s.kind().String()).Msg("hint source failed")
			}
		}
		for _, t := range targets {
			covered[t.Window] = true
		}
		out = append(out, targets...)
		log.Debug().Str("source", s.kind().String()).Int("windows", len(wins)).Int("targets", len(targets)).Float64("ms", ms(time.Since(start))).Msg("hint stage")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
