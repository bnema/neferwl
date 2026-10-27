package hints

import (
	"context"
	"errors"
	"math"

	"github.com/bnema/neferwl/internal/domain"
)

type atspiSource struct{ targets targetSource }

func (*atspiSource) kind() domain.HintSource         { return domain.HintSourceATSPI }
func (*atspiSource) needsFrame(context.Context) bool { return false }
func (s *atspiSource) Close() error {
	if s.targets == nil {
		return nil
	}
	return s.targets.Close()
}
func (s *atspiSource) scan(ctx context.Context, req domain.HintScanRequest, wins []domain.HintWindow, _ *frame) ([]domain.HintTarget, error) {
	if s.targets == nil {
		return nil, errors.New("hints: accessibility source unavailable")
	}
	targets, err := s.targets.Targets(ctx, wins, req.LinksOnly)
	scale := req.Scale
	if scale <= 0 {
		scale = 1
	}
	width, height := int(math.Ceil(float64(req.Width)/scale)), int(math.Ceil(float64(req.Height)/scale))
	out := targets[:0]
	for _, t := range targets {
		if req.LinksOnly && t.Kind != domain.HintLink {
			continue
		}
		x1, y1 := max(0, t.Rect.X), max(0, t.Rect.Y)
		x2, y2 := min(width, t.Rect.X+t.Rect.W), min(height, t.Rect.Y+t.Rect.H)
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		t.Rect = domain.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
		out = append(out, t)
	}
	return out, err
}
