package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/domain"
)

type ConfigReloadPlan struct {
	RestartOnly                                           []string
	KeyboardLayoutChanged, RepeatChanged, HintRestartOnly bool
	Sanitized                                             domain.Config
}

// DiffConfigReload compares already loaded configurations without performing I/O.
func DiffConfigReload(cur, next domain.Config) ConfigReloadPlan {
	p := ConfigReloadPlan{Sanitized: next}
	if !slices.Equal(next.DRM.Devices, cur.DRM.Devices) || !slices.Equal(next.DRM.Ignore, cur.DRM.Ignore) || next.Render.Device != cur.Render.Device || next.Render.MultiGPU != cur.Render.MultiGPU {
		p.RestartOnly = outputRestartOnly
	}
	h, hint := next.Hint, cur.Hint
	p.HintRestartOnly = !slices.Equal(h.Sources, hint.Sources) || h.Enable != hint.Enable || h.Detector != hint.Detector || h.Model != hint.Model || h.Threshold != hint.Threshold
	p.Sanitized.Hint.Enable, p.Sanitized.Hint.Detector, p.Sanitized.Hint.Model, p.Sanitized.Hint.Threshold = hint.Enable, hint.Detector, hint.Model, hint.Threshold
	p.Sanitized.Hint.Sources = slices.Clone(hint.Sources)
	kb, nk := cur.Keyboard, next.Keyboard
	p.KeyboardLayoutChanged = nk.Layout != kb.Layout || nk.Variant != kb.Variant || nk.Options != kb.Options
	p.RepeatChanged = nk.RepeatRate != kb.RepeatRate || nk.RepeatDelay != kb.RepeatDelay
	return p
}

// Shared immutable labels avoid allocating on reload.
var outputRestartOnly = []string{"drm.devices", "drm.ignore", "render.device", "render.multi-gpu"}
