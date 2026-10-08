package core

import (
	"fmt"
	"image"
	"maps"
	"math"
	"reflect"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// OutputOverrides holds runtime-only output settings applied through output
// management on top of the file configuration, which is never mutated. Its
// owner is the backend loop; it is not safe for concurrent use.
type OutputOverrides struct {
	file      ports.Config
	overrides map[string]ports.OutputConfig
	heads     ports.OutputHeads
	headless  bool
}

// NewOutputOverrides starts with no override. Headless outputs keep their
// mode and enabled state.
func NewOutputOverrides(file ports.Config, headless bool) *OutputOverrides {
	return &OutputOverrides{file: file, headless: headless, overrides: map[string]ports.OutputConfig{}}
}

// Headless reports whether the outputs are headless.
func (o *OutputOverrides) Headless() bool { return o.headless }

// Heads is the backend inventory requests are validated against.
func (o *OutputOverrides) Heads() ports.OutputHeads { return o.heads }

// SetHeads records the backend inventory.
func (o *OutputOverrides) SetHeads(heads ports.OutputHeads) { o.heads = heads }

// Snapshot copies the current overrides for a later Restore.
func (o *OutputOverrides) Snapshot() map[string]ports.OutputConfig {
	previous := make(map[string]ports.OutputConfig, len(o.overrides))
	maps.Copy(previous, o.overrides)
	return previous
}

// Restore puts back overrides taken by Snapshot and returns the effective
// configuration.
func (o *OutputOverrides) Restore(previous map[string]ports.OutputConfig) ports.Config {
	o.overrides = previous
	return o.Effective()
}

// Effective is the file configuration with the overrides applied.
func (o *OutputOverrides) Effective() ports.Config {
	cfg := o.file
	cfg.Outputs = slices.Clone(o.file.Outputs)
	// Preserve file order and backend connection order; map iteration would
	// make automatic placement change unpredictably.
	for _, head := range o.heads.Heads {
		entry, ok := o.overrides[head.Info.Name]
		if !ok {
			continue
		}
		index := slices.IndexFunc(cfg.Outputs, func(v ports.OutputConfig) bool { return v.Name == head.Info.Name })
		if index < 0 {
			cfg.Outputs = append(cfg.Outputs, entry)
		} else {
			cfg.Outputs[index] = entry
		}
	}
	return cfg
}

// Reload replaces the file configuration. Runtime overrides are dropped,
// unless only output scales changed (a saved scale bind): then they stay and
// take the new file scales.
func (o *OutputOverrides) Reload(file ports.Config) ports.Config {
	scales, onlyScales := scaleChanges(o.file, file)
	o.file = file
	if !onlyScales {
		clear(o.overrides)
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

// scaleChanges lists the output scales that differ from old to cur, and
// reports whether nothing else differs.
func scaleChanges(old, cur ports.Config) (map[string]float64, bool) {
	scaleOf := func(c ports.Config, name string) float64 {
		for _, o := range c.Outputs {
			if o.Name == name {
				return o.Scale
			}
		}
		return 0
	}
	// Without scales, an output.<name>.scale-only entry is empty: drop it.
	strip := func(c ports.Config) ports.Config {
		c.Outputs = slices.Clone(c.Outputs)
		for i := range c.Outputs {
			c.Outputs[i].Scale = 0
		}
		c.Outputs = slices.DeleteFunc(c.Outputs, func(o ports.OutputConfig) bool {
			return o == ports.OutputConfig{Name: o.Name, ScaleOnly: true, SDRBrightness: ports.DefaultSDRBrightness}
		})
		return c
	}
	if !reflect.DeepEqual(strip(old), strip(cur)) {
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

func (o *OutputOverrides) validate(req ports.OutputApply) error {
	if len(req.Heads) != len(o.heads.Heads) {
		return fmt.Errorf("incomplete head configuration")
	}
	seen := map[string]bool{}
	for _, change := range req.Heads {
		if seen[change.Name] {
			return fmt.Errorf("duplicate head %s", change.Name)
		}
		seen[change.Name] = true
		index := slices.IndexFunc(o.heads.Heads, func(h ports.OutputHead) bool { return h.Info.Name == change.Name })
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
		if o.headless && (change.Enabled != current.Enabled || change.Mode != nil && (current.Current == nil || change.Mode.Width != current.Current.Width || change.Mode.Height != current.Current.Height || change.Mode.RefreshMilli != current.Current.RefreshMilli)) {
			return fmt.Errorf("headless mode and enabled state are fixed")
		}
		if change.Mode != nil && !slices.ContainsFunc(current.Modes, func(m ports.OutputMode) bool {
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
func (o *OutputOverrides) Apply(req ports.OutputApply) (ports.Config, error) {
	if err := o.validate(req); err != nil {
		return ports.Config{}, err
	}
	if req.Test {
		return o.Effective(), nil
	}
	for _, h := range req.Heads {
		entry, ok := o.overrides[h.Name]
		if !ok {
			index := slices.IndexFunc(o.file.Outputs, func(v ports.OutputConfig) bool { return v.Name == h.Name })
			if index >= 0 {
				entry = o.file.Outputs[index]
			} else {
				entry.Name = h.Name
			}
		}
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
			entry.Mode = fmt.Sprintf("%dx%d@%.3f", h.Mode.Width, h.Mode.Height, float64(h.Mode.RefreshMilli)/1000)
		}
		o.overrides[h.Name] = entry
	}
	return o.Effective(), nil
}
