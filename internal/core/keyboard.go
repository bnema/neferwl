package core

import "maps"

// keyboardCandidates is what the keyboard focus picks from, strongest first.
// The layer checks are lazy: they only run while a clicked layer holds the
// keyboard.
type keyboardCandidates struct {
	// exclusive is the mapped top/overlay layer with exclusive keyboard
	// interactivity and the highest ID; grab the topmost grabbing popup.
	exclusive, grab WindowID
	// window is the focused window of the focused output.
	window WindowID
	// layerShown reports whether a layer is still mapped on-demand and
	// visible; windows is the focused window of each output.
	layerShown func(WindowID) bool
	windows    func() map[*screen]WindowID
}

// keyboard owns who holds the keyboard: the rules picking the focus, the
// on-demand layer the user clicked, the focus last told to wayland and the
// shortcuts inhibitor active now.
type keyboard struct {
	// layer is the on-demand layer surface the user clicked; it keeps the
	// keyboard while it stays mapped on-demand and no window focus changes
	// on any output (over, taken at the click): any click elsewhere, bind,
	// activation or new window takes the keyboard back. The pointer moving
	// to another output does not.
	layer WindowID
	over  map[*screen]WindowID
	// sent is the focus last sent to wayland; inhibiting the shortcuts
	// inhibitor active now (0: none).
	sent       WindowID
	inhibiting WindowID
}

// focus is the exclusive layer, else a grabbing popup, else the clicked
// on-demand layer, else the focused window. A clicked layer that is hidden
// (by a fullscreen window) or outlived its window focus loses the keyboard
// for good.
func (k *keyboard) focus(c keyboardCandidates) WindowID {
	if c.exclusive != 0 {
		return c.exclusive
	}
	// A menu with a grab takes the keyboard until it closes.
	if c.grab != 0 {
		return c.grab
	}
	if k.layer != 0 && (!c.layerShown(k.layer) || !maps.Equal(k.over, c.windows())) {
		k.layer, k.over = 0, nil
	}
	if k.layer != 0 {
		return k.layer
	}
	return c.window
}

// clickLayer gives the keyboard to the clicked on-demand layer while the
// window focus stays windows. It reports whether the holder changed.
func (k *keyboard) clickLayer(id WindowID, windows map[*screen]WindowID) bool {
	if k.layer == id {
		return false
	}
	k.layer, k.over = id, windows
	return true
}

// takeBack returns the keyboard from a clicked layer to the windows.
func (k *keyboard) takeBack() { k.layer, k.over = 0, nil }

// inhibit makes the focus the active shortcuts inhibitor when it asks to
// be (wants). It returns the inhibitor to release and the one to activate
// (0: none) when the active one changes.
func (k *keyboard) inhibit(focus WindowID, wants bool) (release, activate WindowID, changed bool) {
	want := WindowID(0)
	if focus != 0 && wants {
		want = focus
	}
	if want == k.inhibiting {
		return 0, 0, false
	}
	release, k.inhibiting = k.inhibiting, want
	return release, want, true
}

// inhibited reports whether the focus gets every key: it is the active
// shortcuts inhibitor.
func (k *keyboard) inhibited(focus WindowID) bool {
	return k.inhibiting != 0 && k.inhibiting == focus
}
