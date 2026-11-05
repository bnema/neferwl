package core

import (
	"fmt"
	"image"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

// OutputOverrides holds runtime-only output settings applied through output
// management on top of the file configuration, which is never mutated. Its
// owner is the backend loop; it is not safe for concurrent use.
type OutputOverrides struct {
	runtime   []domain.OutputConfig
	file      domain.Config
	overrides map[string]domain.OutputConfig
	heads     domain.OutputHeads
	headless  bool
}

// NewOutputOverrides starts with no override. Headless outputs keep their
// mode and enabled state.
func NewOutputOverrides(file domain.Config, headless bool) *OutputOverrides {
	return &OutputOverrides{file: file, headless: headless, overrides: map[string]domain.OutputConfig{}}
}

// Headless reports whether the outputs are headless.
func (o *OutputOverrides) Headless() bool { return o.headless }

// Heads is the backend inventory requests are validated against.
func (o *OutputOverrides) Heads() domain.OutputHeads { return o.heads }

// SetHeads records the backend inventory.
func (o *OutputOverrides) SetHeads(heads domain.OutputHeads) { o.heads = heads }

// Snapshot copies the current overrides for a later Restore.
func (o *OutputOverrides) Snapshot() map[string]domain.OutputConfig {
	previous := make(map[string]domain.OutputConfig, len(o.overrides))
	maps.Copy(previous, o.overrides)
	return previous
}

// Restore puts back overrides taken by Snapshot and returns the effective
// configuration.
func (o *OutputOverrides) Restore(previous map[string]domain.OutputConfig) domain.Config {
	o.overrides = previous
	return o.Effective()
}

// Effective is the file configuration with the overrides applied.
func (o *OutputOverrides) Effective() domain.Config {
	cfg := o.file
	cfg.Outputs = append(slices.Clone(o.file.Outputs), o.runtime...)
	// Virtual overrides also apply before a head is announced or after it stops.
	for i, entry := range cfg.Outputs {
		if override, ok := o.overrides[entry.Name]; entry.Virtual && ok {
			cfg.Outputs[i] = override
		}
	}
	// Preserve file order and backend connection order; map iteration would
	// make automatic placement change unpredictably.
	for _, head := range o.heads.Heads {
		entry, ok := o.overrides[head.Info.Name]
		if !ok {
			continue
		}
		index := slices.IndexFunc(cfg.Outputs, func(v domain.OutputConfig) bool { return v.Name == head.Info.Name })
		if index < 0 {
			cfg.Outputs = append(cfg.Outputs, entry)
		} else {
			cfg.Outputs[index] = entry
		}
	}
	return cfg
}

// Reload replaces the file configuration. Runtime overrides are dropped,
// except modes of unchanged virtual entries. If only output scales changed
// (a saved scale bind), all overrides stay and take the new file scales.
func (o *OutputOverrides) Reload(file domain.Config) domain.Config {
	scales, onlyScales := scaleChanges(o.file, file)
	old := o.file
	o.file = file
	if !onlyScales {
		for name, entry := range o.overrides {
			previous := slices.IndexFunc(old.Outputs, func(v domain.OutputConfig) bool { return v.Name == name && v.Virtual })
			current := slices.IndexFunc(file.Outputs, func(v domain.OutputConfig) bool { return v.Name == name && v.Virtual })
			if previous < 0 || current < 0 || !sameVirtualEntry(withoutKeepFocus(old.Outputs[previous]), withoutKeepFocus(file.Outputs[current])) {
				delete(o.overrides, name)
				continue
			}
			// Only the mode survives an unrelated file edit; other runtime
			// settings retain the existing full-reload reset behavior.
			kept := file.Outputs[current]
			kept.Mode = entry.Mode
			o.overrides[name] = kept
		}
		return o.Effective()
	}
	for name, scale := range scales {
		if entry, ok := o.overrides[name]; ok {
			entry.Scale = scale
			o.overrides[name] = entry
		}
	}
	return o.Effective()
}

// withoutKeepFocus drops keep-focus, a focus policy that leaves the
// output itself (and its runtime mode) as it is.
func withoutKeepFocus(o domain.OutputConfig) domain.OutputConfig {
	o.KeepFocus = false
	return o
}

// scaleChanges lists the output scales that differ from old to cur, and
// reports whether nothing else differs.
func scaleChanges(old, cur domain.Config) (map[string]float64, bool) {
	scaleOf := func(c domain.Config, name string) float64 {
		for _, o := range c.Outputs {
			if o.Name == name {
				return o.Scale
			}
		}
		return 0
	}
	// Without scales, an output.<name>.scale-only entry is empty: drop it.
	strip := func(c domain.Config) domain.Config {
		c.Outputs = slices.Clone(c.Outputs)
		for i := range c.Outputs {
			c.Outputs[i].Scale = 0
		}
		c.Outputs = slices.DeleteFunc(c.Outputs, func(o domain.OutputConfig) bool {
			return sameVirtualEntry(o, domain.OutputConfig{Name: o.Name, ScaleOnly: true, SDRBrightness: domain.DefaultSDRBrightness})
		})
		return c
	}
	if !strip(old).Equal(strip(cur)) {
		return nil, false
	}
	scales := map[string]float64{}
	for _, o := range cur.Outputs {
		if o.Scale != scaleOf(old, o.Name) {
			scales[o.Name] = o.Scale
		}
	}
	return scales, true
}

func (o *OutputOverrides) validate(req domain.OutputApply) error {
	if len(req.Heads) != len(o.heads.Heads) {
		return fmt.Errorf("incomplete head configuration")
	}
	seen := map[string]bool{}
	for _, change := range req.Heads {
		if seen[change.Name] {
			return fmt.Errorf("duplicate head %s", change.Name)
		}
		seen[change.Name] = true
		index := slices.IndexFunc(o.heads.Heads, func(h domain.OutputHead) bool { return h.Info.Name == change.Name })
		if index < 0 {
			return fmt.Errorf("unknown head %s", change.Name)
		}
		current := o.heads.Heads[index]
		if change.Scale < 0 || change.Scale > 4 || (change.Scale > 0 && change.Scale < 1) || math.IsNaN(change.Scale) || math.IsInf(change.Scale, 0) {
			return fmt.Errorf("invalid scale on %s", change.Name)
		}
		if change.Transform != nil && *change.Transform > 7 {
			return fmt.Errorf("invalid transform on %s", change.Name)
		}
		if change.Custom {
			m := change.Mode
			if !current.Info.Virtual || m == nil || m.Width < 1 || m.Width > 16384 || m.Height < 1 || m.Height > 16384 || m.RefreshMilli < 0 {
				return fmt.Errorf("invalid custom mode on %s (virtual heads only, dimensions 1..16384)", change.Name)
			}
		}
		if o.headless && !current.Info.Virtual && strings.HasPrefix(change.Name, "HEADLESS-") && (change.Enabled != current.Enabled || change.Mode != nil && (current.Current == nil || change.Mode.Width != current.Current.Width || change.Mode.Height != current.Current.Height || change.Mode.RefreshMilli != current.Current.RefreshMilli)) {
			return fmt.Errorf("headless mode and enabled state are fixed")
		}
		if change.Mode != nil && !change.Custom && !slices.ContainsFunc(current.Modes, func(m domain.OutputMode) bool {
			return m.Width == change.Mode.Width && m.Height == change.Mode.Height && m.RefreshMilli == change.Mode.RefreshMilli
		}) {
			return fmt.Errorf("unknown mode on %s", change.Name)
		}
	}
	return nil
}

// Apply validates a request against the inventory and, unless it is a
// test, records its changes as overrides. It returns the effective
// configuration.
func (o *OutputOverrides) Apply(req domain.OutputApply) (domain.Config, error) {
	if err := o.validate(req); err != nil {
		return domain.Config{}, err
	}
	if req.Test {
		return o.Effective(), nil
	}
	for _, h := range req.Heads {
		entry, ok := o.overrides[h.Name]
		if !ok {
			index := slices.IndexFunc(o.file.Outputs, func(v domain.OutputConfig) bool { return v.Name == h.Name })
			if index >= 0 {
				entry = o.file.Outputs[index]
			} else {
				entry.Name = h.Name
			}
		}
		// The connected inventory, not a colliding file declaration, owns identity.
		index := slices.IndexFunc(o.heads.Heads, func(head domain.OutputHead) bool { return head.Info.Name == h.Name })
		entry.Virtual = o.heads.Heads[index].Info.Virtual
		entry.Off = !h.Enabled
		if h.Scale > 0 {
			entry.Scale = h.Scale
		}
		if h.Transform != nil {
			entry.Transform = *h.Transform
		}
		if h.Pos != nil {
			entry.Pos = &image.Point{X: h.Pos.X, Y: h.Pos.Y}
		}
		if h.Mode != nil {
			refresh := h.Mode.RefreshMilli
			if h.Custom && refresh == 0 {
				refresh = 60000
			}
			entry.Mode = fmt.Sprintf("%dx%d@%.3f", h.Mode.Width, h.Mode.Height, float64(refresh)/1000)
		}
		o.overrides[h.Name] = entry
	}
	return o.Effective(), nil
}
