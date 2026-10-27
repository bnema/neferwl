package layout

// MaxCells is the largest width a rule or a slot may declare, in cells.
const MaxCells = 16

// Column holds a tiled stack, its width and owner-local layout identity.
type Column struct {
	// Key is the workspace-local column identity; layout state keys
	// per-column data by it.
	Key uint64
	// Carry is what the old layout state let travel with the column to
	// another workspace (State.Removed). The new state takes what it
	// understands in Inserted; the rest, or all of it in a stateless
	// workspace, stays here until the column leaves again.
	Carry any
	// Span is a manually sized width in basis points; zero uses Cells.
	Span int
	// SpanStamp identifies the complete lane to which a manual width belongs.
	SpanStamp uint64
	// Windows is the ordered stack of window IDs.
	Windows []WindowID
	// Cells is the column width in cells; zero shares the available width.
	Cells int
	// FullWidth expands the column to the usable width.
	FullWidth bool
	// Focus is the selected index.
	Focus int
	// Slot is the declared column number (workspace.<name>.column.N) of a
	// slot window; 0 for normal columns.
	Slot int
	// Shares are the row heights set by set-window-height, in basis points, in
	// Windows order. They apply only while there is one per window and
	// they sum to 10000; otherwise the rows share the height equally.
	Shares []int
}

// Selected is the window at the column's Focus index. Like an index
// expression it panics when Focus is outside Windows.
func (c Column) Selected() WindowID { return c.Windows[c.Focus] }

// Overflow says what happens past MaxColumns columns: the config name of a
// layout.
type Overflow string

// Each layout declares its own value next to its type (layout_<name>.go) and
// registers itself in layout_registry.go.

// Lanes is the column state owned by the workspace owner goroutine.
type Lanes struct {
	// Override optionally replaces the registry engine, primarily in tests.
	Override    Layout // Optional test override; production resolves Overflow lazily.
	lstate      State  // Owner-goroutine-only, initialized lazily.
	lstateReady bool   // Distinguishes stateless layouts from uninitialized state.
	// lcache is the layout resolved for lcacheFor. Engine re-resolves it
	// when Overflow no longer matches.
	lcache    Layout
	lcacheFor Overflow
	colSeq    uint64 // Issues workspace-local column identities.
	// Overflow selects the engine by its configuration name.
	Overflow Overflow
	// Columns is the ordered tiled column storage.
	Columns []Column
	// spanSeq issues workspace-local identities for complete lane stamps.
	spanSeq uint64
	// Maximized remembers recently maximized columns by window ID, newest
	// first; stack() filters removed windows at read time.
	Maximized []WindowID
	// Focus is the selected index.
	Focus int
	// View is the settled offset along the layout's content axis.
	View int
	// Output is the effective viewport in monitor coordinates: the whole
	// monitor unless the workspace has a size override, then a centered
	// rectangle no larger than the monitor. Fullscreen fills it.
	Output Rect
	// Usable is the layer reservations of the monitor intersected with Output.
	Usable Rect
	// Gaps is the requested spacing before viewport clamping.
	Gaps int
	// MaxColumns is the visible lane capacity in cells.
	MaxColumns int
	// Fullscreen is the fullscreen window, or zero.
	Fullscreen WindowID
}
