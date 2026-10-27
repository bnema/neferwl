package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestLeaseGuard(t *testing.T) {
	for _, tt := range []struct {
		name          string
		before, after domain.SecurityState
		want          string
	}{
		{name: "initial unprotected"},
		{name: "stable generation", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 4}},
		{name: "protected before", before: domain.SecurityState{Protected: true}, want: "session protected"},
		{name: "protected throughout", before: domain.SecurityState{Generation: 4, Protected: true}, after: domain.SecurityState{Generation: 4, Protected: true}, want: "session protected"},
		{name: "protected during acquisition", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 4, Protected: true}, want: "session transition during lease"},
		{name: "complete protection cycle", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 5}, want: "session transition during lease"},
		{name: "generation regression", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 3}, want: "session transition during lease"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := LeaseGuard(tt.before, tt.after)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.want {
				t.Fatalf("LeaseGuard() = %v, want %q", err, tt.want)
			}
		})
	}
}
