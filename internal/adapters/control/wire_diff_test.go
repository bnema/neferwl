package control

import (
	"encoding/json"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestDiffOmitsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		diff diff
		want string
	}{
		{diff{}, `{}`},
		{diff{FocusFrom: 7, FocusTo: 7}, `{}`},
		{diff{Mapped: []domain.WindowID{2}, Workspaces: []string{"A:2"}}, `{"mapped":[2],"workspaces":["A:2"]}`},
		{diff{FocusFrom: 1, FocusTo: 0, FocusChanged: true}, `{"focus_from":1,"focus_to":0,"focus_changed":true}`},
		{diff{Unmapped: []domain.WindowID{1}}, `{"unmapped":[1]}`},
	} {
		data, err := json.Marshal(tc.diff)
		if err != nil || string(data) != tc.want {
			t.Fatalf("%s %v want %s", data, err, tc.want)
		}
	}
}
