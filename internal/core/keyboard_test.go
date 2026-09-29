package core

import "testing"

func TestKeyboardFocusOrder(t *testing.T) {
	a := &screen{}
	windows := map[*screen]WindowID{a: 1}
	shown := true
	cand := keyboardCandidates{window: 1, layerShown: func(WindowID) bool { return shown }, windows: func() map[*screen]WindowID { return windows }}
	var k keyboard
	if got := k.focus(cand); got != 1 {
		t.Fatalf("focus = %d, want the window", got)
	}
	if !k.clickLayer(5, map[*screen]WindowID{a: 1}) || k.clickLayer(5, windows) {
		t.Fatal("clickLayer change report")
	}
	if got := k.focus(cand); got != 5 {
		t.Fatalf("focus = %d, want the clicked layer", got)
	}
	grab := cand
	grab.grab = 7
	if got := k.focus(grab); got != 7 {
		t.Fatalf("focus = %d, want the grabbing popup", got)
	}
	grab.exclusive = 9
	if got := k.focus(grab); got != 9 {
		t.Fatalf("focus = %d, want the exclusive layer", got)
	}
	// The grab closed: the clicked layer still holds the keyboard.
	if got := k.focus(cand); got != 5 {
		t.Fatalf("focus = %d, want the clicked layer back", got)
	}
	// A window focus change on any output takes it back for good.
	windows = map[*screen]WindowID{a: 2}
	cand.window = 2
	if got := k.focus(cand); got != 2 {
		t.Fatalf("focus = %d, want the new window", got)
	}
	windows = map[*screen]WindowID{a: 1}
	cand.window = 1
	if got := k.focus(cand); got != 1 || k.layer != 0 {
		t.Fatalf("focus = %d, layer %d; the layer must not come back", got, k.layer)
	}
}

func TestKeyboardHiddenLayerLosesFocus(t *testing.T) {
	windows := map[*screen]WindowID{}
	shown := false
	cand := keyboardCandidates{window: 1, layerShown: func(WindowID) bool { return shown }, windows: func() map[*screen]WindowID { return windows }}
	var k keyboard
	k.clickLayer(5, map[*screen]WindowID{})
	if got := k.focus(cand); got != 1 {
		t.Fatalf("focus = %d, want the window", got)
	}
	shown = true
	if got := k.focus(cand); got != 1 {
		t.Fatalf("focus = %d, the hidden layer must not come back", got)
	}
	k.clickLayer(5, map[*screen]WindowID{})
	k.takeBack()
	if got := k.focus(cand); got != 1 {
		t.Fatalf("focus = %d after takeBack", got)
	}
}

func TestKeyboardInhibit(t *testing.T) {
	var k keyboard
	if _, _, changed := k.inhibit(1, false); changed {
		t.Fatal("no inhibitor changed")
	}
	if rel, act, changed := k.inhibit(1, true); rel != 0 || act != 1 || !changed || !k.inhibited(1) {
		t.Fatalf("activate = %d, %d, %v", rel, act, changed)
	}
	if _, _, changed := k.inhibit(1, true); changed {
		t.Fatal("same inhibitor changed")
	}
	if rel, act, changed := k.inhibit(2, true); rel != 1 || act != 2 || !changed {
		t.Fatalf("switch = %d, %d, %v", rel, act, changed)
	}
	if rel, act, changed := k.inhibit(0, true); rel != 2 || act != 0 || !changed || k.inhibited(0) {
		t.Fatalf("release = %d, %d, %v", rel, act, changed)
	}
}
