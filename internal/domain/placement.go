package domain

// PlacementTrace records a first-map placement decision, not a frame.
type PlacementTrace struct {
	Window                                  WindowID
	Path, Output                            string
	Workspace                               uint64
	Launcher, Cover                         WindowID
	WorkspaceFocusChanged, SeatFocusChanged bool
}
