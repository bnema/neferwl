package libinput

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestBoundPointers(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "desktop"}, Width: 100, Height: 100, Scale: 1}, {Info: domain.OutputInfo{Name: "tv"}, X: 300, Width: 100, Height: 80, Scale: 2, Isolated: true, Inputs: []string{"Mouse*"}, InputOrder: 1}}
	p := newPointer(l)
	p.addDevice(1, "Mouse passthrough")
	p.addDevice(2, "other")
	bound := p.forDevice(1)
	m := bound.move(10000, 10000)
	if m.X != 399 || m.Y != 79 {
		t.Fatal(m)
	}
	b := bound.bounds()
	if b.X != 300 || b.W != 100 || b.H != 80 {
		t.Fatal(b)
	}
	x, y := bound.set(float64(b.X)+float64(b.W)/2, float64(b.Y)+float64(b.H)/2)
	if x != 350 || y != 40 {
		t.Fatalf("absolute %v,%v", x, y)
	}
	var output string
	bound.moved(func(name string, _, _ float64, _ bool) { output = name }, true)
	if output != "tv" {
		t.Fatal(output)
	}
	unbound := p.forDevice(2)
	if m := unbound.move(10000, 0); m.X != 99 {
		t.Fatal(m)
	}
	if p.forDevice(1).x != 350 {
		t.Fatal("bound position lost")
	}
	p.constrain(domain.PointerConstraint{Mode: domain.ConstraintLock, X: 350, Y: 40})
	if bound.constraint.Mode != domain.ConstraintLock || p.constraint.Mode == domain.ConstraintLock {
		t.Fatal("constraint not active-only")
	}
	p.forDevice(2)
	if bound.constraint.Mode == domain.ConstraintLock {
		t.Fatal("old constraint retained")
	}
	l[1].Width = 20
	p.setLayout(l)
	if bound.x != 319 {
		t.Fatal(bound.x)
	}
	p.setLayout(l[:1])
	if p.forDevice(1) != p {
		t.Fatal("absent target did not unbind")
	}
	p.removeDevice(1)
}

func TestBindingConfigPrecedence(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "later"}, Width: 10, Height: 10, Inputs: []string{"*"}, InputOrder: 2}, {Info: domain.OutputInfo{Name: "first"}, X: 100, Width: 10, Height: 10, Inputs: []string{"*"}, InputOrder: 0}}
	p := newPointer(l)
	p.addDevice(1, "mouse")
	if p.forDevice(1).layout[0].Info.Name != "first" {
		t.Fatal("not config order")
	}
	l[1].Inputs[0] = "different"
	if p.all[0].Inputs[0] != "*" {
		t.Fatal("layout aliases caller")
	}
}
