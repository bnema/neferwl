package domain

// LockSurfacePlacement is a validated mapped lock role, never a desktop window.
type LockSurfacePlacement struct {
	ID            WindowID
	Output        string
	Width, Height int
}
