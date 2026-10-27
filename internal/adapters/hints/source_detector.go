package hints

import (
	"context"
	"errors"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
)

type detectorSource struct{ det detector }

func (*detectorSource) kind() domain.HintSource         { return domain.HintSourceDetector }
func (*detectorSource) needsFrame(context.Context) bool { return true }
func (s *detectorSource) Close() error                  { return s.det.Close() }
func (s *detectorSource) scan(ctx context.Context, req domain.HintScanRequest, wins []domain.HintWindow, fr *frame) ([]domain.HintTarget, error) {
	var out []domain.HintTarget
	var scanErr error
	scale := req.Scale
	if scale <= 0 {
		scale = 1
	}
	raw, prepared := 0, 0
	for _, win := range wins {
		crop := windowCrop(win.Bounds(), scale, req.Width, req.Height)
		if crop.Empty() {
			continue
		}
		regions := tileRegions(crop.Dx(), crop.Dy())
		log := logging.For(ctx, "hints")
		log.Debug().Uint64("window", uint64(win.ID)).
			Int("crop_x", crop.Min.X).Int("crop_y", crop.Min.Y).
			Int("crop_width", crop.Dx()).Int("crop_height", crop.Dy()).
			Msg("hint detector request")
		dets, err := s.det.Detect(ctx, detFrame{m: fr.m, stride: fr.stride, crop: crop, regions: regions})
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			if errors.Is(err, errDetector) {
				continue
			}
			break // the child was dropped; never restart it within this scan
		}
		raw += len(dets)
		dets = prepareDetections(dets, regions, crop.Dx(), crop.Dy())
		prepared += len(dets)
		// Clamp in crop-local pixels, offset in physical pixels, then round once.
		dets = dropCutBoxes(dets, regions)
		for i := range dets {
			dets[i].x1 += float32(crop.Min.X)
			dets[i].x2 += float32(crop.Min.X)
			dets[i].y1 += float32(crop.Min.Y)
			dets[i].y2 += float32(crop.Min.Y)
		}
		targets := mergeTargets(targetsOf(dets, scale, req.Width, req.Height))
		for _, t := range targets {
			if !win.Contains(t.Rect.X+t.Rect.W/2, t.Rect.Y+t.Rect.H/2) {
				continue
			}
			t.Source, t.Kind, t.Window = domain.HintSourceDetector, domain.HintButton, win.ID
			out = append(out, t)
		}
	}
	log := logging.For(ctx, "hints")
	log.Info().Int("raw_boxes", raw).Int("prepared_boxes", prepared).Msg("hint detector scan")
	return out, scanErr
}
