package workspace

import "testing"

func TestHoverFocusable(t *testing.T) {
	// tiles 1 and 2; stash [3]; native float 4 (small, not covering).
	build := func() *Workspace {
		w := workspace()
		w.AddWindow(1)
		w.AddWindow(2)
		w.AddWindow(3)
		w.FocusID(3)
		w.ToggleWindowStash()
		w.AddFloating(4, 20, 20)
		return w
	}
	tests := []struct {
		name  string
		setup func(w *Workspace)
		id    WindowID
		want  bool
	}{
		{"float", func(*Workspace) {}, 4, true},
		{"shown stash member", func(*Workspace) {}, 3, true},
		{"hidden stash member", func(w *Workspace) { w.stashHidden = true }, 3, false},
		{"tile without stash or floats", func(w *Workspace) { w.Stash, w.Floats = nil, nil }, 1, true},
		{"tile while stash shown", func(*Workspace) {}, 1, false},
		{"tile while stash hidden", func(w *Workspace) { w.stashHidden = true }, 1, true},
		{"tile with covering float", func(w *Workspace) {
			w.Stash = nil
			w.Floats[0].W, w.Floats[0].H = w.Usable.W, w.Usable.H
		}, 1, false},
		{"tile with covering float below", func(w *Workspace) {
			w.Stash = nil
			w.Floats[0].W, w.Floats[0].H = w.Usable.W, w.Usable.H
			w.Floats[0].below = true
		}, 1, true},
		{"unknown", func(*Workspace) {}, 99, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := build()
			tt.setup(w)
			if tt.name == "tile with covering float" && !w.coversFloat(w.Floats[0]) {
				t.Fatal("float must cover")
			}
			if got := w.HoverFocusable(tt.id); got != tt.want {
				t.Fatalf("HoverFocusable(%d) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestHoverFocusableNoAlloc(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddFloating(2, 20, 20)
	for _, id := range []WindowID{1, 2} {
		if n := testing.AllocsPerRun(100, func() { w.HoverFocusable(id) }); n != 0 {
			t.Fatalf("HoverFocusable(%d) allocs = %v, want 0", id, n)
		}
	}
}
