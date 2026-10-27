package app

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerowrap"
)

func logOutputApplied(heads []domain.HeadChange, log zerowrap.Logger) {
	for _, h := range heads {
		e := log.Info().Str("output", h.Name).Bool("enabled", h.Enabled).Float64("scale", h.Scale)
		if h.Transform != nil {
			e = e.Uint8("transform", uint8(*h.Transform))
		}
		e.Msg("output configuration applied")
	}
}
