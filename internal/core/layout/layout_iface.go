package layout

// View is the read-only snapshot a layout computes from. Slices
// alias owner storage: never retained, never written.
type View struct {
	// Columns is the ordered tiled column storage.
	Columns []Column
	// Focus is the selected index.
	Focus int
	// View is the settled content-axis offset in pixels.
	View int // settled offset along the content axis
	// MaxColumns is the visible lane capacity in cells.
	MaxColumns int // lane capacity (cells per view or columns per lane)
	// Usable is the viewport after layer reservations.
	Usable Rect
	// Output is the viewport filled by fullscreen windows.
	Output Rect
	// Gap is the effective spacing in pixels.
	Gap int
	// Fullscreen is the fullscreen window, or zero.
	Fullscreen WindowID
	// State is per-layout data, nil for stateless layouts.
	State State // per-layout state, nil for stateless layouts
	// Layout is the resolved engine used by shared geometry helpers.
	Layout Layout // the resolved layout; shared helpers dispatch through it

	// facts are the width facts of this view, set by WithFacts. A loop over
	// columns computes them once and calls Layout.ColumnWidth per column, so
	// an override of ColumnWidth is honored without an O(n) cost per column.
	// They describe the view they were set on: a modified copy sets them again.
	facts WidthFacts
}

// RectOpts selects a geometry variant.
type RectOpts struct {
	// IgnoreFullWidth measures hidden columns without maximization.
	IgnoreFullWidth bool // overview hidden-columns card
	// Shift is the presentation offset; zero gives settled geometry.
	Shift int // presentation offset, 0 = settled
}

// Traits are constant capabilities.
type Traits interface {
	// Name returns the configuration key.
	Name() Overflow // registry key, config value
	// Axes returns the content and workspace navigation axes.
	Axes() (content, workspace Axis) // scroll axis, workspace-step axis
	// Scrolls reports whether the content axis can slide.
	Scrolls() bool // content axis can slide
	// EqualCells reports whether lanes share width by cells.
	EqualCells() bool // lanes share width equally: sized by cells
	// MaximizeHides reports whether maximization hides other columns.
	MaximizeHides() bool // a maximized column hides the others
	// PinsFullscreen reports whether fullscreen immediately covers the output.
	PinsFullscreen() bool // fullscreen covers the output at once and hides the rest
	// ResetsMaximize reports whether switching layouts clears maximization.
	ResetsMaximize() bool // switching to this layout unmaximizes every column
	// OverridesWidth enables dispatch to the engine’s custom column widths.
	OverridesWidth() bool // columnWidth differs from columnsBase's: generic loops dispatch to it
}

// Shape says how columns group into lanes and how the user moves
// through them.
type Shape interface {
	// Bands reports whether columns group into independent bands.
	Bands() bool // lanes are bands: a maximized column keeps its own, floats have none
	// LaneGestures reports whether content swipes step between lanes.
	LaneGestures() bool // a swipe along the content axis steps between lanes, not workspaces
	// Spiral reports whether excess columns subdivide the last slot.
	Spiral() bool // past capacity the last slot subdivides; directional focus follows the screen
	// LaneRow reports whether columns share a row by cells.
	LaneRow(v View) bool // a lane's columns share its width by cells, in one row
}

// Geometry turns columns into rects.
type Geometry interface {
	// ColumnRects appends screen-space column rectangles using the supplied storage.
	ColumnRects(v View, dst []Rect, o RectOpts) []Rect // one rect per column, screen coords
	// ColumnWidth returns a column’s width in logical pixels.
	ColumnWidth(v View, i int, ignoreFullWidth bool) int
	// ScrollTarget returns the settled offset that shows the selected column.
	ScrollTarget(v View) int // View that shows v.Focus (scroll())
	// SnapPoints builds sorted gesture destinations in dst[:0].
	SnapPoints(v View, dst []float64) []float64 // sorted views a swipe may land on, built in dst[:0]
	// SnapFocus selects the column at a gesture destination.
	SnapFocus(v View, view int, forward bool) int // column to focus at view
}

// LaneSet groups columns into lanes (cascade bands; one lane otherwise;
// a tree layout projects its leaves).
type LaneSet interface {
	// LaneOf returns the lane containing a column.
	LaneOf(v View, i int) int
	// LaneBounds returns inclusive column bounds; first > last denotes no lane.
	LaneBounds(v View, lane int) (first, last int) // first > last: no lane
	// LaneCount returns the number of lanes.
	LaneCount(v View) int
	// LaneNeighbor selects the closest column in an adjacent lane, or -1.
	LaneNeighbor(v View, d int) int // -1 past the edge
}

// Navigator decides where focus goes.
type Navigator interface {
	// FocusStep selects a column along the content axis, or -1 at an edge.
	FocusStep(v View, dir int) int // column left/right of the focus; -1: hand to the neighbor monitor
	// LaneStep selects a column across lanes, or -1 when none exists.
	LaneStep(v View, dir int) int // column up/down past the focused one; -1: none
}

// InsertKind says how an inserted column relates to the lanes around it.
type InsertKind uint8

const (
	// InsertDefault places a column in its existing lane slot.
	InsertDefault InsertKind = iota // the column takes its slot
	// InsertNewLane starts an independent lane.
	InsertNewLane // it starts a lane of its own
	// InsertJoinDisplaced inherits the displaced column’s lane position.
	InsertJoinDisplaced // it takes the lane position of the column it displaces
)

// Placer decides where columns and windows go.
type Placer interface {
	// SpawnAt returns the insertion position and lane relationship of a new column.
	SpawnAt(v View) (at int, kind InsertKind) // after the focused column/lane
	// CoveredSpawnAt returns the insertion position under a covering window.
	CoveredSpawnAt(v View) int // under a covering window (ADR 011)
	// Full reports whether a new column must be expelled to a neighbor.
	Full(v View) bool // no room: expel to neighbor (fixed)
	// MoveAllowed reports whether columns may swap positions.
	MoveAllowed(v View, from, to int) bool // MoveColumn/drag swap inside a lane
	// MoveLane moves a window to the lane in dir and reports a change.
	MoveLane(w Edit, dir int) bool // MoveWindow past the column: to the lane dir away
	// AdoptFloat isolates a formerly floating window in a tiled lane.
	AdoptFloat(w Edit, id WindowID) bool
	// Isolate gives a window its own lane before fullscreen.
	Isolate(w Edit, id WindowID) bool // before id goes fullscreen: a lane of its own
}

// Sizer mutates column widths and maximization after Workspace guards.
type Sizer interface {
	// Grow widens the selected column; cycle resets a fully grown lane.
	Grow(w Edit, cycle bool)
	// Shrink narrows the selected column.
	Shrink(w Edit)
	// ToggleFullWidth toggles maximization after workspace guards.
	ToggleFullWidth(w Edit)
}

// State owns per-layout data and follows column edits. Workspace calls
// the hooks after it changed Columns; a layout switch drops the state.
type State interface {
	// Inserted follows Columns[at] being added; carry is what removed
	// returned in the workspace the column came from, nil if none. It
	// returns the carry it did not consume (nil once consumed): that stays
	// on the column and the next workspace's removed or takeColumn passes it
	// on. A stateless workspace consumes nothing and keeps it too.
	Inserted(w *Lanes, at int, kind InsertKind, carry any) (unconsumed any)
	// Removed follows the column col leaving index at; the returned carry
	// travels with the column to its next workspace. Nil leaves the carry
	// the column already holds.
	Removed(at int, col Column) (carry any)
	// Settle refreshes layout state after column edits, before measuring.
	Settle(w *Lanes) // before scroll(): refresh keyed data, drop stale spans
}

// Overview projects a workspace for the overview.
type Overview interface {
	// PreviewTiles projects columns for overview, reusing the supplied buffers.
	PreviewTiles(v View, dst []Placement, rowBuf []Rect) (tiles []Placement, span int, sel Rect)
	// PreviewLanes reports whether preview Y coordinates encode lanes.
	PreviewLanes() bool // tiles carry their lane in Y, one Usable.H apart
}

// Layout composes the engine hooks stored in the registry.
type Layout interface {
	Traits
	Shape
	Geometry
	LaneSet
	Navigator
	Placer
	Sizer
	Overview
	// NewState constructs independent per-owner state, or nil.
	NewState() State // nil for stateless layouts
}

// Edit captures focus once at the start of a workspace layout operation.
type Edit struct {
	*Lanes
	// Focused is the window selected when the operation began.
	Focused WindowID
}
