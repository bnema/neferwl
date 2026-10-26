package app

import (
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

func TestReopenLimiter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds []int
		want    []bool
	}{
		{"three then refused", []int{0, 10, 20, 30}, []bool{true, true, true, false}},
		{"expires after 61 seconds", []int{0, 0, 0, 0, 61}, []bool{true, true, true, false, true}},
		{"inclusive 60 second boundary", []int{0, 0, 0, 60, 61}, []bool{true, true, true, false, true}},
		{"only expired entries dropped", []int{0, 30, 40, 61, 62, 91}, []bool{true, true, true, true, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := portsmocks.NewMockClock(t)
			base := time.Unix(1000, 0)
			for _, seconds := range tc.seconds {
				clock.EXPECT().Now().Return(base.Add(time.Duration(seconds) * time.Second)).Once()
			}
			l := reopenLimiter{clock: clock}
			for i, want := range tc.want {
				if got := l.allow(); got != want {
					t.Fatalf("attempt %d at %ds: got %v want %v", i, tc.seconds[i], got, want)
				}
			}
		})
	}
}
