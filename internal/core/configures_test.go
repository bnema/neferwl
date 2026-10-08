package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestConfiguresNext(t *testing.T) {
	area := Rect{W: 1000, H: 800}
	tiled := Placement{ID: 1, Rect: Rect{W: 500, H: 800}}
	target := configureTarget{output: "A", view: whole(area), focused: true, client: Rect{W: 496, H: 796}}
	tests := []struct {
		name  string
		first *Placement // sent before p, with target
		p     Placement
		t     configureTarget
		want  ports.ConfigureWindow
		send  bool
	}{
		{name: "tiled", p: tiled, t: target, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Activated: true, Output: "A", Visible: true}, send: true},
		{name: "unchanged", first: &tiled, p: tiled, t: target, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Activated: true, Output: "A", Visible: true}},
		{name: "scrolled off", p: Placement{ID: 1, Rect: Rect{X: 1000, W: 500, H: 800}}, t: target, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Activated: true, Output: "A"}, send: true},
		{name: "native float", p: Placement{ID: 1, Rect: Rect{W: 300, H: 200}, Floating: true}, t: target, want: ports.ConfigureWindow{ID: 1, Activated: true, Floating: true, Output: "A", Visible: true}, send: true},
		{name: "imposed float", p: Placement{ID: 1, Rect: Rect{W: 300, H: 200}, Floating: true}, t: configureTarget{output: "A", view: whole(area), client: Rect{W: 300, H: 200}, imposed: true}, want: ports.ConfigureWindow{ID: 1, Width: 300, Height: 200, Floating: true, Output: "A", Visible: true}, send: true},
		{name: "fullscreen float", p: Placement{ID: 1, Rect: area, Floating: true, Fullscreen: true}, t: configureTarget{output: "A", view: whole(area), client: area}, want: ports.ConfigureWindow{ID: 1, Width: 1000, Height: 800, Fullscreen: true, Output: "A", Visible: true}, send: true},
		{name: "hidden new", p: Placement{ID: 1, Hidden: true, Floating: true}, t: target, want: ports.ConfigureWindow{ID: 1, Floating: true, Output: "A"}, send: true},
		{name: "hidden keeps size", first: &tiled, p: Placement{ID: 1, Hidden: true}, t: configureTarget{output: "B"}, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Output: "B"}, send: true},
		{name: "preview new", p: Placement{ID: 1, Rect: Rect{W: 250, H: 200}, Preview: 0.5}, t: target, want: ports.ConfigureWindow{ID: 1, Width: 500, Height: 400, Activated: true, Output: "A", Visible: true}, send: true},
		{name: "fullscreen preview keeps size and state", first: &Placement{ID: 1, Rect: area, Floating: true, Fullscreen: true}, p: Placement{ID: 1, Rect: Rect{W: 100, H: 80}, Preview: 0.1}, t: configureTarget{output: "A", view: whole(area)}, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Fullscreen: true, Output: "A", Visible: true}, send: true},
		{name: "hidden float made fullscreen previews fullscreen", first: &Placement{ID: 1, Hidden: true, Floating: true}, p: Placement{ID: 1, Rect: Rect{W: 100, H: 80}, Preview: 0.1, Fullscreen: true}, t: configureTarget{output: "A", view: whole(area)}, want: ports.ConfigureWindow{ID: 1, Width: 1000, Height: 800, Fullscreen: true, Output: "A", Visible: true}, send: true},
		{name: "preview keeps size", first: &tiled, p: Placement{ID: 1, Rect: Rect{W: 100, H: 100}, Preview: 0.5}, t: configureTarget{output: "A", view: whole(area)}, want: ports.ConfigureWindow{ID: 1, Width: 496, Height: 796, Output: "A", Visible: true}, send: true},
		{name: "new float preview stays floating", p: Placement{ID: 1, Rect: Rect{W: 600, H: 480}, Preview: 0.6, Floating: true}, t: target, want: ports.ConfigureWindow{ID: 1, Floating: true, Activated: true, Output: "A", Visible: true}, send: true},
		{name: "covering float preview keeps native size", first: &Placement{ID: 1, Rect: area, Floating: true}, p: Placement{ID: 1, Rect: Rect{W: 600, H: 480}, Preview: 0.6, Floating: true}, t: target, want: ports.ConfigureWindow{ID: 1, Floating: true, Activated: true, Output: "A", Visible: true}},
		{name: "covering float peek keeps native size", first: &Placement{ID: 1, Rect: area, Floating: true}, p: Placement{ID: 1, Rect: Rect{X: 20, Y: 20, W: 600, H: 480}, Preview: 0.6, Floating: true, Peek: true}, t: target, want: ports.ConfigureWindow{ID: 1, Floating: true, Activated: true, Output: "A", Visible: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newConfigures()
			if tt.first != nil {
				v, _ := s.next(*tt.first, target)
				s.mark(v)
			}
			got, send := s.next(tt.p, tt.t)
			if got != tt.want || send != tt.send {
				t.Fatalf("next = %+v, %v; want %+v, %v", got, send, tt.want, tt.send)
			}
		})
	}
}

// A window awaiting an answer gets its configure once, even unchanged.
func TestConfiguresAnswer(t *testing.T) {
	s := newConfigures()
	p, target := Placement{ID: 1, Rect: Rect{W: 10, H: 10}}, configureTarget{output: "A"}
	v, _ := s.next(p, target)
	s.mark(v)
	s.answer[1] = true
	if _, send := s.next(p, target); !send {
		t.Fatal("answer not sent")
	}
	s.mark(v)
	if _, send := s.next(p, target); send {
		t.Fatal("answered twice")
	}
}

func TestConfiguresPruneForgetsUnseen(t *testing.T) {
	s := newConfigures()
	for _, id := range []WindowID{1, 2} {
		v, _ := s.next(Placement{ID: id}, configureTarget{})
		s.mark(v)
	}
	s.prune()
	s.next(Placement{ID: 1}, configureTarget{})
	s.prune()
	if _, ok := s.sent[2]; ok {
		t.Fatal("window out of the layout kept")
	}
	if _, send := s.next(Placement{ID: 1}, configureTarget{}); send {
		t.Fatal("window still in the layout configured again")
	}
	if _, send := s.next(Placement{ID: 2}, configureTarget{}); !send {
		t.Fatal("returning window not configured")
	}
	// A request from a window out of the layout is forgotten too.
	s.answer[9] = true
	s.prune()
	if s.answer[9] {
		t.Fatal("answer for a window out of the layout kept")
	}
}
