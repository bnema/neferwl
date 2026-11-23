package domain

// BindSource tells which path ran a dispatched action.
type BindSource string

const (
	BindSourceControl BindSource = "control"
	BindSourceKey     BindSource = "key"
	BindSourceRepeat  BindSource = "repeat"
	BindSourceGesture BindSource = "gesture"
	BindSourceEdge    BindSource = "edge"
)

// InputTrace carries core → debug log a record of what input did. Core cannot
// log; the app does.
type InputTrace interface{ inputTrace() }

// BindDispatched records one action run by input. Combo is the key combination
// (never typed text), or the gesture, that triggered it. WorkspaceBefore and
// WorkspaceAfter are the 1-based position of the active workspace on Output
// (the focused output when the action began) before and after the action;
// OutputAfter is the focused output once it ran. Window is the focused window
// on Output when the action began, 0 if none. Notable reports that the action
// changes windows or layout, not only focus or view, and was not a key
// repeat; the app logs notable dispatches at info.
type BindDispatched struct {
	Combo           string
	Action          string
	Output          string
	OutputAfter     string
	WorkspaceBefore int
	WorkspaceAfter  int
	Source          BindSource
	Window          WindowID
	Notable         bool
}

func (BindDispatched) inputTrace() {}

// SeatInputReset records the input state dropped when the seat went
// inactive: how many keys and modifiers were held, and how many buttons.
type SeatInputReset struct {
	Keys, Mods, Buttons int
}

func (SeatInputReset) inputTrace() {}
