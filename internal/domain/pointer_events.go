package domain

// ConstraintMode is how a pointer constraint holds the pointer.
type ConstraintMode uint8

const (
	ConstraintNone ConstraintMode = iota
	// ConstraintLock keeps the pointer still: only relative motion flows.
	ConstraintLock
	// ConstraintConfine keeps the pointer inside Rect.
	ConstraintConfine
)
