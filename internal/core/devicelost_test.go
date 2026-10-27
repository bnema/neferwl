package core

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
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
			l := ReopenLimiter{Clock: clock}
			for i, want := range tc.want {
				if got := l.Allow(); got != want {
					t.Fatalf("attempt %d at %ds: got %v want %v", i, tc.seconds[i], got, want)
				}
			}
		})
	}
}

func TestLostFormats(t *testing.T) {
	a := domain.DMABufFormat{Format: 1, Modifier: 2}
	b := domain.DMABufFormat{Format: 1, Modifier: 3}
	for _, tc := range []struct {
		advertised, current []domain.DMABufFormat
		want                int
	}{
		{nil, nil, 0},
		{[]domain.DMABufFormat{a, b}, []domain.DMABufFormat{b, a}, 0},
		{[]domain.DMABufFormat{a, b}, []domain.DMABufFormat{a}, 1},
		{[]domain.DMABufFormat{a, b}, nil, 2},
		{nil, []domain.DMABufFormat{a}, 0},
	} {
		if got := LostFormats(tc.advertised, tc.current); got != tc.want {
			t.Fatalf("got %d want %d", got, tc.want)
		}
	}
}
