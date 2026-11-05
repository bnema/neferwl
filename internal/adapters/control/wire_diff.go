package control

import "github.com/bnema/neferwl/internal/domain"

// diff is the wire form of domain.StateDiff: unchanged sections are omitted,
// but both focus endpoints (including zero for no focus) are kept whenever
// focus changed.
type diff struct {
	Mapped       []domain.WindowID `json:"mapped,omitempty"`
	Unmapped     []domain.WindowID `json:"unmapped,omitempty"`
	FocusFrom    *domain.WindowID  `json:"focus_from,omitempty"`
	FocusTo      *domain.WindowID  `json:"focus_to,omitempty"`
	FocusChanged bool              `json:"focus_changed,omitempty"`
	Workspaces   []string          `json:"workspaces,omitempty"`
}

func wireDiff(d domain.StateDiff) diff {
	w := diff{Mapped: d.Mapped, Unmapped: d.Unmapped, Workspaces: d.Workspaces}
	if d.FocusChanged {
		w.FocusFrom, w.FocusTo, w.FocusChanged = &d.FocusFrom, &d.FocusTo, true
	}
	return w
}
