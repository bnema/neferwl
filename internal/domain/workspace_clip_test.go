package domain

import "testing"

func TestRectContains(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 20, H: 20}
	for o, want := range map[Rect]bool{r: true, {X: 15, Y: 15, W: 5, H: 5}: true, {X: 9, Y: 10, W: 5, H: 5}: false, {X: 25, Y: 25, W: 6, H: 5}: false} {
		if got := r.Contains(o); got != want {
			t.Errorf("%+v contains %+v = %v, want %v", r, o, got, want)
		}
	}
}
