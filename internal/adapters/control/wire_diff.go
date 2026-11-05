package control

import "encoding/json"

// MarshalJSON omits unchanged sections, but retains both focus endpoints
// (including zero for no focus) whenever focus changed.
func (d diff) MarshalJSON() ([]byte, error) {
	type wire struct {
		Mapped       any      `json:"mapped,omitempty"`
		Unmapped     any      `json:"unmapped,omitempty"`
		FocusFrom    any      `json:"focus_from,omitempty"`
		FocusTo      any      `json:"focus_to,omitempty"`
		FocusChanged bool     `json:"focus_changed,omitempty"`
		Workspaces   []string `json:"workspaces,omitempty"`
	}
	w := wire{Workspaces: d.Workspaces}
	if len(d.Mapped) > 0 {
		w.Mapped = d.Mapped
	}
	if len(d.Unmapped) > 0 {
		w.Unmapped = d.Unmapped
	}
	if d.FocusChanged {
		w.FocusFrom = d.FocusFrom
		w.FocusTo = d.FocusTo
		w.FocusChanged = true
	}
	return json.Marshal(w)
}
