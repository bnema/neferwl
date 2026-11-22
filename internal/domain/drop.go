package domain

// DropAction is what releasing a dragged tile on a drop anchor does.
// DropAuto resolves to a layout-dependent action (edges only).
type DropAction uint8

const (
	DropAuto DropAction = iota
	DropNone
	DropSwap
	DropColumn
	DropStack
	DropScroll
	DropWorkspace
	DropStash
	DropFloat
	DropTab
)

var dropActionNames = [...]string{
	DropAuto: "auto", DropNone: "none", DropSwap: "swap", DropColumn: "column",
	DropStack: "stack", DropScroll: "scroll", DropWorkspace: "workspace",
	DropStash: "stash", DropFloat: "float", DropTab: "tab",
}

// ParseDropAction maps a config value to its action.
func ParseDropAction(s string) (DropAction, bool) {
	for i, n := range dropActionNames {
		if n == s {
			return DropAction(i), true
		}
	}
	return DropAuto, false
}

// String is the config name of the action.
func (a DropAction) String() string {
	if int(a) < len(dropActionNames) {
		return dropActionNames[a]
	}
	return ""
}

// Label is the preview label: empty for none and auto.
func (a DropAction) Label() string {
	if a == DropAuto || a == DropNone {
		return ""
	}
	return a.String()
}

// Hover reports whether the action fires while the pointer rests on it.
func (a DropAction) Hover() bool { return a == DropScroll || a == DropWorkspace }

// DropSide is a side of a window or an output edge.
type DropSide uint8

const (
	DropSideCenter DropSide = iota
	DropSideLeft
	DropSideRight
	DropSideTop
	DropSideBottom
)

// DropPolicy maps anchors to actions. Window and Edge are indexed by
// DropSide; Edge[DropSideCenter] is unused. Fields that were never set inherit
// from a base policy through Over.
type DropPolicy struct {
	Window [5]DropAction
	Gap    DropAction
	Edge   [5]DropAction
	set    uint16 // bits 0-4 Window, 5 Gap, 6-10 Edge
}

const dropGapBit = 5

func dropEdgeBit(s DropSide) uint { return 6 + uint(s) }

// SetWindow sets the action of a window side.
func (p *DropPolicy) SetWindow(s DropSide, a DropAction) {
	p.Window[s] = a
	p.set |= 1 << uint(s)
}

// SetGap sets the action of column gaps.
func (p *DropPolicy) SetGap(a DropAction) {
	p.Gap = a
	p.set |= 1 << dropGapBit
}

// SetEdge sets the action of a workspace edge. DropSideCenter is ignored.
func (p *DropPolicy) SetEdge(s DropSide, a DropAction) {
	if s == DropSideCenter {
		return
	}
	p.Edge[s] = a
	p.set |= 1 << dropEdgeBit(s)
}

// Over returns p with every unset field taken from base.
func (p DropPolicy) Over(base DropPolicy) DropPolicy {
	out := base
	for s := range p.Window {
		if p.set&(1<<uint(s)) != 0 {
			out.Window[s] = p.Window[s]
		}
		if p.set&(1<<dropEdgeBit(DropSide(s))) != 0 {
			out.Edge[s] = p.Edge[s]
		}
	}
	if p.set&(1<<dropGapBit) != 0 {
		out.Gap = p.Gap
	}
	out.set = p.set | base.set
	return out
}

// DefaultDropPolicy is the built-in policy: the center tabs, left/right
// insert a column, top/bottom stack, gaps insert a column and edges follow
// the layout axes.
func DefaultDropPolicy() DropPolicy {
	var p DropPolicy
	p.SetWindow(DropSideCenter, DropTab)
	p.SetWindow(DropSideLeft, DropColumn)
	p.SetWindow(DropSideRight, DropColumn)
	p.SetWindow(DropSideTop, DropStack)
	p.SetWindow(DropSideBottom, DropStack)
	p.SetGap(DropColumn)
	for s := DropSideLeft; s <= DropSideBottom; s++ {
		p.SetEdge(s, DropAuto)
	}
	return p
}

// DropZoneKind is the kind of a drop policy field: a window side, the
// gap, or an output edge.
type DropZoneKind uint8

const (
	DropZoneWindow DropZoneKind = iota
	DropZoneGap
	DropZoneEdge
)

// DropAllowed reports whether action a is allowed for the policy field of
// kind k on side s (ignored for the gap; an edge has no center).
func DropAllowed(k DropZoneKind, s DropSide, a DropAction) bool {
	switch k {
	case DropZoneWindow:
		switch s {
		case DropSideCenter:
			return a == DropTab || a == DropSwap || a == DropNone
		case DropSideLeft, DropSideRight:
			return a == DropColumn || a == DropSwap || a == DropNone
		case DropSideTop, DropSideBottom:
			return a == DropStack || a == DropSwap || a == DropNone
		}
	case DropZoneGap:
		return a == DropColumn || a == DropNone
	case DropZoneEdge:
		if s == DropSideCenter || s > DropSideBottom {
			return false
		}
		switch a {
		case DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone:
			return true
		}
	}
	return false
}

// EdgeSet reports whether edge s was explicitly set.
func (p DropPolicy) EdgeSet(s DropSide) bool {
	return s != DropSideCenter && p.set&(1<<dropEdgeBit(s)) != 0
}
