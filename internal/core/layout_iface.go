package core

// layoutView is the read-only snapshot a layout computes from. Slices
// alias Workspace storage: never retained, never written.
type layoutView struct {
	Columns    []Column
	Focus      int
	View       int // settled offset along the content axis
	MaxColumns int // lane capacity (cells per view or columns per lane)
	Usable     Rect
	Output     Rect
	Gap        int
	Fullscreen WindowID
	State      layoutState // per-layout state, nil for stateless layouts
	layout     layout      // the resolved layout; shared helpers dispatch through it

	// facts are the width facts of this view, set by withFacts. A loop over
	// columns computes them once and calls layout.columnWidth per column, so
	// an override of columnWidth is honored without an O(n) cost per column.
	// They describe the view they were set on: a modified copy sets them again.
	facts widthFacts
}

// rectOpts selects a geometry variant.
type rectOpts struct {
	IgnoreFullWidth bool // overview hidden-columns card
	Shift           int  // presentation offset, 0 = settled
}

// layoutTraits are constant capabilities. Mock: newMocklayoutTraits.
type layoutTraits interface {
	name() Overflow                        // registry key, config value
	axes() (content, workspace layoutAxis) // scroll axis, workspace-step axis
	scrolls() bool                         // content axis can slide
	equalCells() bool                      // lanes share width equally: sized by cells
	maximizeHides() bool                   // a maximized column hides the others
	pinsFullscreen() bool                  // fullscreen covers the output at once and hides the rest
	resetsMaximize() bool                  // switching to this layout unmaximizes every column
	overridesWidth() bool                  // columnWidth differs from columnsBase's: generic loops dispatch to it
}

// layoutShape says how columns group into lanes and how the user moves
// through them. Mock: newMocklayoutShape.
type layoutShape interface {
	bands() bool               // lanes are bands: a maximized column keeps its own, floats have none
	laneGestures() bool        // a swipe along the content axis steps between lanes, not workspaces
	spiral() bool              // past capacity the last slot subdivides; directional focus follows the screen
	laneRow(v layoutView) bool // a lane's columns share its width by cells, in one row
}

// layoutGeometry turns columns into rects. Mock: newMocklayoutGeometry.
type layoutGeometry interface {
	columnRects(v layoutView, dst []Rect, o rectOpts) []Rect // one rect per column, screen coords
	columnWidth(v layoutView, i int, ignoreFullWidth bool) int
	scrollTarget(v layoutView) int                      // View that shows v.Focus (scroll())
	snapPoints(v layoutView, dst []float64) []float64   // sorted views a swipe may land on, built in dst[:0]
	snapFocus(v layoutView, view int, forward bool) int // column to focus at view
}

// layoutLanes groups columns into lanes (cascade bands; one lane otherwise;
// a tree layout projects its leaves). Mock: newMocklayoutLanes.
type layoutLanes interface {
	laneOf(v layoutView, i int) int
	laneBounds(v layoutView, lane int) (first, last int) // first > last: no lane
	laneCount(v layoutView) int
	laneNeighbor(v layoutView, d int) int // -1 past the edge
}

// layoutNavigator decides where focus goes. Mock: newMocklayoutNavigator.
type layoutNavigator interface {
	focusStep(v layoutView, dir int) int // column left/right of the focus; -1: hand to the neighbor monitor
	laneStep(v layoutView, dir int) int  // column up/down past the focused one; -1: none
}

// insertKind says how an inserted column relates to the lanes around it.
type insertKind uint8

const (
	insertDefault       insertKind = iota // the column takes its slot
	insertNewLane                         // it starts a lane of its own
	insertJoinDisplaced                   // it takes the lane position of the column it displaces
)

// layoutPlacer decides where columns and windows go. Mock: newMocklayoutPlacer.
type layoutPlacer interface {
	spawnAt(v layoutView) (at int, kind insertKind) // after the focused column/lane
	coveredSpawnAt(v layoutView) int                // under a covering window (ADR 011)
	full(v layoutView) bool                         // no room: expel to neighbor (fixed)
	moveAllowed(v layoutView, from, to int) bool    // MoveColumn/drag swap inside a lane
	moveLane(w edit, dir int) bool                  // MoveWindow past the column: to the lane dir away
	adoptFloat(w edit, id WindowID) bool
	isolate(w edit, id WindowID) bool // before id goes fullscreen: a lane of its own
}

// layoutSizer mutates column widths and maximization after Workspace guards.
// Mock: newMocklayoutSizer.
type layoutSizer interface {
	grow(w edit, cycle bool)
	shrink(w edit)
	toggleFullWidth(w edit)
}

// layoutState owns per-layout data and follows column edits. Workspace calls
// the hooks after it changed Columns; a layout switch drops the state.
// Mock: newMocklayoutState.
type layoutState interface {
	// inserted follows Columns[at] being added; carry is what removed
	// returned in the workspace the column came from, nil if none. It
	// returns the carry it did not consume (nil once consumed): that stays
	// on the column and the next workspace's removed or takeColumn passes it
	// on. A stateless workspace consumes nothing and keeps it too.
	inserted(w *lanes, at int, kind insertKind, carry any) (unconsumed any)
	// removed follows the column col leaving index at; the returned carry
	// travels with the column to its next workspace. Nil leaves the carry
	// the column already holds.
	removed(at int, col Column) (carry any)
	settle(w *lanes) // before scroll(): refresh keyed data, drop stale spans
}

// layoutOverview projects a workspace for the overview. Mock: newMocklayoutOverview.
type layoutOverview interface {
	previewTiles(v layoutView, dst []Placement, rowBuf []Rect) (tiles []Placement, span int, sel Rect)
	previewLanes() bool // tiles carry their lane in Y, one Usable.H apart
}

// layout is what the registry stores. Mock: newMocklayout (composed, used by
// Workspace-level tests through w.lay).
type layout interface {
	layoutTraits
	layoutShape
	layoutGeometry
	layoutLanes
	layoutNavigator
	layoutPlacer
	layoutSizer
	layoutOverview
	newState() layoutState // nil for stateless layouts
}

// edit captures focus once at the start of a workspace layout operation.
type edit struct {
	*lanes
	Focused WindowID
}
