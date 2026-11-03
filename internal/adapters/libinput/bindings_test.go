package libinput

import (
	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

func TestBoundPointers(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "desktop"}, Width: 100, Height: 100, Scale: 1}, {Info: domain.OutputInfo{Name: "tv"}, X: 300, Width: 100, Height: 80, Scale: 2, Isolated: true, Devices: []domain.InputDeviceID{1}, InputOrder: 1}}
	p := newPointer(l)
	p.addDevice(1, 1)
	p.addDevice(2, 2)
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

func TestResolvedBindingIgnoresLayoutOrder(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "later"}, Width: 10, Height: 10, InputOrder: 2}, {Info: domain.OutputInfo{Name: "first"}, X: 100, Width: 10, Height: 10, Devices: []domain.InputDeviceID{1}, InputOrder: 0}}
	p := newPointer(l)
	p.addDevice(1, 1)
	if p.forDevice(1).layout[0].Info.Name != "first" {
		t.Fatal("resolved binding ignored")
	}
	l[1].Devices[0] = 2
	if p.all[1].Devices[0] != 1 {
		t.Fatal("layout aliases caller")
	}
}

func TestInterleavedDevicePositionsAndRebinding(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "desktop"}, Width: 100, Height: 100}, {Info: domain.OutputInfo{Name: "tv"}, X: 300, Width: 100, Height: 100, Isolated: true, Devices: []domain.InputDeviceID{1, 2, 3}}}
	p := newPointer(l)
	for dev := uintptr(1); dev <= 4; dev++ {
		p.addDevice(dev, domain.InputDeviceID(dev))
	}
	for round := 0; round < 5; round++ {
		for dev := uintptr(1); dev <= 4; dev++ {
			p.forDevice(dev).move(float64(dev), 0)
		}
	}
	for dev := uintptr(1); dev <= 3; dev++ {
		if got, want := p.forDevice(dev).x, 350+float64(dev)*5; got != want {
			t.Fatalf("device %d: %v want %v", dev, got, want)
		}
	}
	if p.forDevice(4).x != 70 {
		t.Fatal("shared pointer position lost")
	}
	p.forDevice(2)
	p.setLayout(l[:1])
	if p.active != nil || len(p.devices) != 0 || p.forDevice(2) != p {
		t.Fatal("stale binding after active output removal")
	}
	// Core's next resolved snapshot rebinds one device, not the others.
	l[0].Devices = []domain.InputDeviceID{2}
	p.setLayout(l[:1])
	if p.forDevice(2) == p || p.forDevice(1) != p {
		t.Fatal("reload rebind not applied")
	}
	p.removeDevice(2)
	p.addDevice(2, 5) // same native handle, new producer identity
	if p.forDevice(2) != p {
		t.Fatal("old identity binding resurrected")
	}
	l[0].Devices = []domain.InputDeviceID{5}
	p.setLayout(l[:1])
	if p.forDevice(2) == p {
		t.Fatal("new identity not bound")
	}
}

func TestPointerSwitchReappliesCurrentConstraint(t *testing.T) {
	p := newPointer(domain.Layout{{Width: 100, Height: 100, Devices: []domain.InputDeviceID{1, 2}}})
	p.addDevice(1, 1)
	p.addDevice(2, 2)
	for _, mode := range []domain.ConstraintMode{domain.ConstraintLock, domain.ConstraintConfine} {
		p.forDevice(1)
		constraint := domain.PointerConstraint{Mode: mode, Rect: domain.Rect{W: 80, H: 80}, X: 20, Y: 20}
		p.constrain(constraint)
		q := p.forDevice(2)
		if q.constraint.Mode != mode {
			t.Fatalf("unchanged current constraint not applied: %+v", q.constraint)
		}
	}
}

func TestSwitchMotionUsesOwnerPosition(t *testing.T) {
	l := domain.Layout{{Width: 100, Height: 100}, {X: 300, Width: 100, Height: 100, Isolated: true, Devices: []domain.InputDeviceID{1}}}
	p := newPointer(l)
	p.addDevice(1, 1)
	p.addDevice(2, 2)
	p.addDevice(3, 3)
	shared := p.forDevice(2)
	shared.move(10, 0)
	p.switchMotion(shared, shared, 2, ports.PointerMotion{})
	previous := p.active
	other := p.forDevice(3)
	if other != shared {
		t.Fatal("unbound mice do not share position")
	}
	if _, ok := p.switchMotion(previous, other, 3, ports.PointerButton{}); ok {
		t.Fatal("shared switch emitted motion")
	}
	previous = p.active
	bound := p.forDevice(1)
	bound.set(390, 30)
	l[1].X = 500
	l[1].Width = 20
	p.setLayout(l)
	motion, ok := p.switchMotion(previous, bound, 1, ports.PointerButton{})
	if !ok || motion.X != 500 || motion.Y != 30 || motion.DX != 0 || motion.Device != 1 {
		t.Fatalf("rebound sync: %+v %v", motion, ok)
	}
	p.constrain(domain.PointerConstraint{Warp: true, X: 510, Y: 20})
	bound.move(3, 0)
	p.forDevice(2)
	p.forDevice(1)
	if bound.x != 513 || p.currentConstraint.Warp || bound.constraint.Warp {
		t.Fatalf("warp replay: %v %+v", bound.x, bound.constraint)
	}
}

func TestSwitchMotionOrderingAndLastBindingRemoval(t *testing.T) {
	p := newPointer(domain.Layout{{Width: 100, Height: 100, Devices: []domain.InputDeviceID{1}}})
	p.addDevice(1, 1)
	p.addDevice(2, 2)
	previous := p.forDevice(2)
	target := p.forDevice(1)
	event := ports.PointerButton{Device: 1, Pressed: true}
	motion, ok := p.switchMotion(previous, target, 1, event)
	if !ok {
		t.Fatal("missing switch motion")
	}
	f := newForwarder(zerowrap.Default())
	f.push(motion)
	f.push(event)
	if _, ok := f.queue[0].(ports.PointerMotion); !ok {
		t.Fatal(f.queue)
	}
	if _, ok := f.queue[1].(ports.PointerButton); !ok {
		t.Fatal(f.queue)
	}
	p.setLayout(domain.Layout{{Width: 100, Height: 100}})
	previous = p.active
	target = p.forDevice(1)
	if target != p {
		t.Fatal("last binding retained")
	}
	if _, ok := p.switchMotion(previous, target, 1, event); !ok {
		t.Fatal("last-binding removal requires one sync")
	}
	if _, ok := p.switchMotion(target, target, 1, event); ok {
		t.Fatal("steady no-bindings emitted sync")
	}
}

func TestLayoutSyncCoordinatesTimeAndCursor(t *testing.T) {
	l := domain.Layout{{Info: domain.OutputInfo{Name: "tv"}, X: 100, Width: 100, Height: 100, Devices: []domain.InputDeviceID{1}}}
	p := newPointer(l)
	p.addDevice(1, 1)
	q := p.forDevice(1)
	p.switchMotion(nil, q, 1, ports.PointerMotion{})
	l[0].X = 400
	p.setLayout(l)
	event := ports.PointerAxis{Device: 1, Time: 123}
	m, ok := p.switchMotion(q, q, 1, event)
	if !ok || m.X != 450 || m.Y != 50 || m.Time != 123 {
		t.Fatalf("sync=%+v %v", m, ok)
	}
	var name string
	var x, y float64
	q.moved(func(n string, a, b float64, _ bool) { name, x, y = n, a, b }, true)
	if name != "tv" || x != 50 || y != 50 {
		t.Fatalf("cursor %s %v,%v", name, x, y)
	}
	if _, ok := p.switchMotion(q, q, 1, event); ok {
		t.Fatal("sync not consumed")
	}
	f := newForwarder(zerowrap.Default())
	f.push(ports.PointerMotion{Device: 1, Time: 100})
	f.push(m)
	got, _, _, _ := queuedMotion(f.queue[0])
	if got.Time != 123 {
		t.Fatal(got)
	}
	f.push(ports.PointerMotion{Device: 1, Time: 0})
	got, _, _, _ = queuedMotion(f.queue[0])
	if got.Time != 123 {
		t.Fatal("valid timestamp lost")
	}
}
