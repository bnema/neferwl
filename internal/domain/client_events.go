package domain

// ResizeEdges are the window edges a pointer resize moves, as bit flags
// (the xdg_toplevel resize_edge values).
type ResizeEdges uint32

const (
	ResizeTop    ResizeEdges = 1
	ResizeBottom ResizeEdges = 2
	ResizeLeft   ResizeEdges = 4
	ResizeRight  ResizeEdges = 8
)
