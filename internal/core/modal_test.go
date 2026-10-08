package core

import "testing"

// A modal dialog takes the focus its parent gets from the user: a click,
// an activation or a focus move onto the parent. A move away from the
// dialog reaches the parent once, and the next one goes on.
func TestModalDialogTakesParentFocus(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddWindow(2)
	w.AddDialog(3, 1, 20, 10)
	w.SetModal(3, true)
	focus := func(want WindowID) {
		t.Helper()
		if id, _ := w.Focused(); id != want {
			t.Fatalf("focus %d, want %d", id, want)
		}
	}
	focus(3)
	w.Click(2)
	focus(2)
	w.Click(1)
	focus(3)
	w.Click(2)
	w.Activate(1)
	focus(3)
	// Away from the dialog: the parent, then on.
	w.FocusColumn(-1)
	focus(1)
	// A move that goes nowhere (the left edge) leaves it there.
	w.FocusColumn(-1)
	focus(1)
	w.FocusColumn(1)
	focus(2)
	// Back onto the parent from elsewhere: the dialog.
	w.FocusColumn(-1)
	focus(3)

	// Not modal: the parent keeps the focus it gets.
	w.SetModal(3, false)
	w.Click(1)
	focus(1)
	// Turning modal is a client request: it moves no focus, the next
	// user focus on the parent does.
	w.SetModal(3, true)
	focus(1)
	w.Click(2)
	w.Click(1)
	focus(3)
	// A modal dialog of the dialog blocks both.
	w.AddDialog(4, 3, 10, 5)
	w.SetModal(4, true)
	w.Click(2)
	w.Click(1)
	focus(4)
	w.RemoveWindow(4)
	// Picking the parent's card in the overview is a user focus too.
	w.Click(2)
	w.apply(stackItem{kind: stackColumns}, 1)
	focus(3)
	// Closed: the parent is reachable again.
	w.RemoveWindow(3)
	w.Click(2)
	w.Click(1)
	focus(1)
}
