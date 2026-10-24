package hints

import (
	"context"
	"errors"

	"github.com/bnema/neferwl/internal/ports"
)

type atspiSource struct{ targets targetSource }

func (*atspiSource) kind() ports.HintSource { return ports.HintSourceATSPI }
func (*atspiSource) needsFrame() bool       { return false }
func (s *atspiSource) Close() error {
	if s.targets == nil {
		return nil
	}
	return s.targets.Close()
}
func (s *atspiSource) scan(ctx context.Context, req ports.HintScanRequest, wins []ports.HintWindow, _ *frame) ([]ports.HintTarget, error) {
	if s.targets == nil {
		return nil, errors.New("hints: accessibility source unavailable")
	}
	targets, err := s.targets.Targets(ctx, wins, req.LinksOnly)
	out := targets[:0]
	for _, t := range targets {
		if !req.LinksOnly || t.Kind == ports.HintLink {
			out = append(out, t)
		}
	}
	return out, err
}
