package motion

import (
	"math"
	"testing"
	"time"
)

func TestSlideLifecycle(t *testing.T) {
	var s Slide
	if s.Busy() {
		t.Fatal("zero slide is busy")
	}
	s.Off = 100
	if !s.Busy() {
		t.Fatal("finger offset is not busy")
	}
	start := time.Unix(10, 0)
	s.Motion = NewMotion(ViewSpring(s.Off, 0), start, 1)
	s.Step(start.Add(20 * time.Millisecond))
	if s.Off <= 0 || s.Off >= 100 || !s.Busy() {
		t.Fatalf("spring did not advance: %+v", s)
	}
	s.Step(start.Add(time.Hour))
	if s != (Slide{}) || s.Busy() {
		t.Fatalf("settled slide was not cleared: %+v", s)
	}
	s.Off = 30
	s.Step(start)
	if s.Off != 30 {
		t.Fatal("idle step moved the fingers' offset")
	}
	s.Stop()
	if s != (Slide{}) {
		t.Fatal("stop did not clear slide")
	}
}

func TestMotionSampleAndScale(t *testing.T) {
	start := time.Unix(10, 0)
	m := NewMotion(ViewSpring(100, 20), start, 2)
	before := m
	now := start.Add(30 * time.Millisecond)
	pos, vel := m.SampleAt(now)
	if m != before {
		t.Fatal("sampling mutated the motion")
	}
	copy := m
	want, _ := copy.At(now)
	if pos != want || vel != copy.Velocity() {
		t.Fatal("sampling disagrees with advancing a copy")
	}
	m.Scale(3)
	got, speed := m.SampleAt(now)
	if math.Abs(got-3*pos) > 1e-9 || math.Abs(speed-3*vel) > 1e-9 {
		t.Fatalf("scale changed progress: position %v, speed %v", got, speed)
	}
	var idle Motion
	idle.Scale(3)
	if idle != (Motion{}) {
		t.Fatal("scaling started an idle motion")
	}
}
