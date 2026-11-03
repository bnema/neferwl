package core

import "github.com/bnema/neferwl/internal/domain"

// Virtuals returns desired virtual outputs in file order, with runtime modes.
// Physical connector names take precedence over virtual declarations.
func (o *OutputOverrides) Virtuals() []domain.OutputConfig {
	var want []domain.OutputConfig
	for _, entry := range o.Effective().Outputs {
		if entry.Virtual && !o.physicalName(entry.Name) {
			want = append(want, entry)
		}
	}
	return want
}

func (o *OutputOverrides) physicalName(name string) bool {
	for _, head := range o.heads.Heads {
		if head.Info.Name == name && !head.Info.Virtual {
			return true
		}
	}
	return false
}

// Virtuals exposes the desired set to the backend owner loop.
func (a *OutputApply) Virtuals() []domain.OutputConfig { return a.state.Virtuals() }

// checkVirtuals queues each collision once while it remains present. Inventory
// scans must not flood the log; resolving and reintroducing a collision warns again.
func (a *OutputApply) checkVirtuals() {
	current := map[string]bool{}
	for _, entry := range a.state.file.Outputs {
		if !entry.Virtual || !a.state.physicalName(entry.Name) {
			continue
		}
		current[entry.Name] = true
		if !a.collisions[entry.Name] {
			a.warnings = append(a.warnings, domain.OutputWarning{Name: entry.Name, Message: "virtual output ignored: name collides with a physical head"})
		}
	}
	a.collisions = current
}

// TakeWarnings drains configuration diagnostics independently of protocol replies.
func (a *OutputApply) TakeWarnings() []domain.OutputWarning {
	warnings := a.warnings
	a.warnings = nil
	return warnings
}

// Use Config's value-aware equality (not pointer identity for Pos).
func sameVirtualEntry(a, b domain.OutputConfig) bool {
	return (domain.Config{Outputs: []domain.OutputConfig{a}}).Equal(domain.Config{Outputs: []domain.OutputConfig{b}})
}
