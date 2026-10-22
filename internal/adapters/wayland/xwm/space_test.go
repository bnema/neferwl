package xwm

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func out(x, y, w, h int) ports.OutputPlacement {
	return ports.OutputPlacement{X: x, Y: y, Width: w, Height: h, Scale: 1}
}

func TestSpaceLayouts(t *testing.T) {
	tests := []struct {
		name         string
		l            ports.Layout
		w, h         int
		lx, ly, x, y int
	}{
		{"single", ports.Layout{out(0, 0, 1920, 1080)}, 1920, 1080, 10, 20, 10, 20},
		{"side by side", ports.Layout{out(0, 0, 1920, 1080), out(1920, 0, 1280, 1024)}, 3200, 1080, 2000, 5, 2000, 5},
		{"left-of negative origin", ports.Layout{out(-640, 0, 640, 480), out(0, 0, 1920, 1080)}, 2560, 1080, -630, 20, 10, 20},
		{"above", ports.Layout{out(0, -500, 800, 500), out(0, 0, 800, 600)}, 800, 1100, 3, -400, 3, 100},
		{"below", ports.Layout{out(0, 0, 800, 600), out(0, 600, 800, 400)}, 800, 1000, 3, 700, 3, 700},
		{"hole nearest", ports.Layout{out(0, 0, 100, 100), out(300, 0, 100, 100)}, 400, 100, 120, 10, 120, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s Space
			if !s.Set(tc.l) {
				t.Fatal("first Set not changed")
			}
			if w, h := s.Size(); w != tc.w || h != tc.h {
				t.Fatalf("size %dx%d, want %dx%d", w, h, tc.w, tc.h)
			}
			x, y := s.ToX(tc.lx, tc.ly)
			if x != tc.x || y != tc.y {
				t.Fatalf("ToX = %d,%d want %d,%d", x, y, tc.x, tc.y)
			}
			if lx, ly := s.ToLayout(x, y); lx != tc.lx || ly != tc.ly {
				t.Fatalf("round trip = %d,%d want %d,%d", lx, ly, tc.lx, tc.ly)
			}
			if s.Set(tc.l) {
				t.Fatal("identical Set reported changed")
			}
		})
	}
}

func TestSpaceHoleAndRegion(t *testing.T) {
	var s Space
	s.Set(ports.Layout{out(0, 0, 100, 100), out(300, 0, 100, 100)})
	if _, ok := s.RegionAtLayout(200, 50); ok {
		t.Fatal("hole must have no region")
	}
	r, ok := s.RegionAtLayout(350, 50)
	if !ok || r.X.X != 300 {
		t.Fatalf("region = %+v %v", r, ok)
	}
}

func TestSpaceGenAndEmpty(t *testing.T) {
	var s Space
	if x, y := s.ToX(5, 6); x != 5 || y != 6 {
		t.Fatalf("empty ToX = %d,%d", x, y)
	}
	s.Set(ports.Layout{out(0, 0, 10, 10)})
	g := s.Gen()
	s.Set(ports.Layout{out(0, 0, 10, 10)})
	if s.Gen() != g {
		t.Fatal("gen bumped without change")
	}
	s.Set(ports.Layout{out(5, 0, 10, 10)})
	if s.Gen() == g {
		t.Fatal("gen not bumped")
	}
}

func TestSpaceSeventeenOutputs(t *testing.T) {
	l := make(ports.Layout, 17)
	for i := range l {
		l[i] = out(i*100, 0, 100, 100)
	}
	var s Space
	s.Set(l)
	if s.Dropped() != 1 {
		t.Fatalf("dropped = %d", s.Dropped())
	}
	if w, _ := s.Size(); w != 1600 {
		t.Fatalf("width = %d", w)
	}
	if x, _ := s.ToX(1550, 5); x != 1550 {
		t.Fatalf("ToX = %d", x)
	}
}

func TestSpaceAllocations(t *testing.T) {
	l := ports.Layout{out(-640, 0, 640, 480), out(0, 0, 1920, 1080)}
	var s Space
	s.Set(l)
	n := testing.AllocsPerRun(100, func() {
		s.Set(l)
		x, y := s.ToX(-600, 10)
		x, y = s.ToLayout(x, y)
		_, _ = s.RegionAtLayout(x, y)
		_, _ = s.Size()
	})
	if n != 0 {
		t.Fatalf("allocs = %v", n)
	}
}
