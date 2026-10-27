package supervisor

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
)

func (run *outputsRun) onReaderFailure(failure readerFailure) (error, bool) {
	path := failure.card.Path()
	for _, action := range run.policy.OnReaderFailure(domain.CardName(filepath.Base(path)), failure.card.removed, deviceNodeExists(path), errors.Is(failure.err, unix.ENODEV), failure.err) {
		switch a := action.(type) {
		case core.ActRemoveCard:
			run.log.Warn().Err(failure.err).Str("card", path).Msg("card reader failed, device gone")
			run.removeCard(a.Card)
		case core.ActExit:
			if errors.Is(a.Err, core.ErrRenderGPURemoved) {
				run.log.Error().Err(failure.err).Str("card", path).Msg("render GPU removed")
				return errors.Join(fmt.Errorf("%w: %s: %w", core.ErrRenderGPURemoved, path, failure.err), run.set.Wait()), true
			}
			return errors.Join(failure.err, run.set.Wait()), true
		}
	}

	return nil, false
}
