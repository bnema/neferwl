// Package statewire defines the public state JSON format shared by adapters.
package statewire

import (
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
)

// State is the file format.
type State struct {
	Output  string   `json:"output"`
	Outputs []Output `json:"outputs"`
	Window  *Window  `json:"window"`
	Windows []Window `json:"windows"`
	// Workspaces lists every workspace of every output.
	Workspaces []Workspace `json:"workspaces"`
}

// Workspace is one workspace and its tiled columns, left to right. ID is
// the workspace_id string of Output. Number is 0 for a hidden workspace.
// Visible means on screen on its output.
type Workspace struct {
	Focused  domain.WindowID `json:"focused"`
	ID       string          `json:"id"`
	Number   int             `json:"number"`
	Name     string          `json:"name"`
	Output   string          `json:"output"`
	Visible  bool            `json:"visible"`
	Overflow string          `json:"overflow"`
	Cover    domain.WindowID `json:"cover"`
	Columns  []Column        `json:"columns"`
}

// Column is the area of one column in logical pixels of its output, view
// offset applied (it may be off-screen), and its windows top to bottom.
type Column struct {
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Windows []Tile `json:"windows"`
}

// Tile is one tiled window and its rectangle.
type Tile struct {
	ID     uint64 `json:"id"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Output is one output: its active numbered workspace (0 while a hidden one
// is shown), how many numbered workspaces it has, and the name and ID of the
// workspace on screen. WorkspaceID is the same string ext_workspace_handle_v1
// sends as its id (package workspaceid): "name:<name>" for a configured
// workspace, else "<launch prefix>-<core ID>".
type Output struct {
	HDR         domain.OutputHDRState `json:"hdr"`
	Name        string                `json:"name"`
	Active      int                   `json:"active"`
	Count       int                   `json:"count"`
	Workspace   string                `json:"workspace"`
	WorkspaceID string                `json:"workspace_id"`
}

// Window is one mapped window.
type Window struct {
	SDRExpansion SDRExpansion `json:"sdr_expansion"`
	ID           uint64       `json:"id"`
	AppID        string       `json:"app_id"`
	PID          int          `json:"pid"`
	Output       string       `json:"output"`
	Workspace    int          `json:"workspace"`
	// WorkspaceID is the ID of the window's workspace, numbered or hidden
	// (see Output.WorkspaceID).
	WorkspaceID string `json:"workspace_id"`
	Visible     bool   `json:"visible"`
	// Sandbox is the client's security context app id, else its engine,
	// "" when not sandboxed.
	Sandbox string `json:"sandbox"`
	// ContentType is "photo", "video", "game", or "" without a hint.
	ContentType string `json:"content_type"`
	// Xwayland is set for an X11 window served by the satellite session.
	Xwayland bool `json:"xwayland"`
	// IdleInhibit is set while the window keeps the session from idling.
	IdleInhibit bool `json:"idle_inhibit"`
	// ShortcutsInhibited is set while the window asks to receive compositor
	// shortcuts.
	ShortcutsInhibited bool `json:"shortcuts_inhibited"`
	// PointerConstraint is "locked" or "confined" while the window holds the
	// pointer constraint, "" otherwise.
	PointerConstraint string `json:"pointer_constraint"`
	// Floating is set for native floats (dialogs) and stashed windows.
	Floating bool `json:"floating"`
	// Fullscreen is set for the fullscreen windows of its workspace: the
	// cover (Workspace.Cover) and held members.
	Fullscreen bool `json:"fullscreen"`
	// StashIndex is the 1-based place of a stashed window in its
	// workspace's stash of StashCount windows; both are 0 outside it.
	StashIndex int `json:"stash_index"`
	StashCount int `json:"stash_count"`
	// Column and Row are the 1-based place of a tiled window; both are 0
	// for floating windows.
	Column int `json:"column"`
	Row    int `json:"row"`
	// Hidden is set for a stashed window while the stash is hidden.
	Hidden bool `json:"hidden"`
}

// FromDomain converts a domain snapshot to the script-facing JSON format.
func FromDomain(st domain.State, ids *workspaceid.IDs) State {
	out := State{Output: st.Output, Outputs: make([]Output, 0, len(st.Outputs)), Windows: make([]Window, 0, len(st.Windows)), Workspaces: make([]Workspace, 0, len(st.Workspaces))}
	for _, o := range st.Outputs {
		out.Outputs = append(out.Outputs, Output{HDR: o.HDR, Name: o.Name, Active: o.Active, Count: o.Count, Workspace: o.Workspace, WorkspaceID: ids.ID(o.WorkspaceID, o.Workspace)})
	}
	for _, w := range st.Windows {
		out.Windows = append(out.Windows, window(w, ids))
	}
	for _, w := range st.Workspaces {
		ws := Workspace{Focused: w.Focused, ID: ids.ID(w.ID, w.Name), Number: w.Number, Name: w.Name, Output: w.Output, Visible: w.Visible, Overflow: w.Overflow, Cover: w.Cover, Columns: make([]Column, 0, len(w.Columns))}
		for _, c := range w.Columns {
			col := Column{X: c.Rect.X, Y: c.Rect.Y, Width: c.Rect.W, Height: c.Rect.H, Windows: make([]Tile, 0, len(c.Windows))}
			for _, t := range c.Windows {
				col.Windows = append(col.Windows, Tile{ID: uint64(t.ID), X: t.Rect.X, Y: t.Rect.Y, Width: t.Rect.W, Height: t.Rect.H})
			}
			ws.Columns = append(ws.Columns, col)
		}
		out.Workspaces = append(out.Workspaces, ws)
	}
	if st.Window != nil {
		w := window(*st.Window, ids)
		out.Window = &w
	}
	return out
}

func window(w domain.WindowState, ids *workspaceid.IDs) Window {
	return Window{SDRExpansion: expansion(w.SDRExpansion), ID: uint64(w.ID), AppID: w.AppID, PID: w.PID, Sandbox: w.Sandbox, ContentType: w.ContentType, Xwayland: w.Xwayland, Output: w.Output, Workspace: w.Workspace, WorkspaceID: ids.ID(w.WorkspaceID, w.WorkspaceName), Visible: w.Visible, IdleInhibit: w.IdleInhibit, ShortcutsInhibited: w.ShortcutsInhibited, PointerConstraint: w.PointerConstraint, Floating: w.Floating, Fullscreen: w.Fullscreen, StashIndex: w.StashIndex, StashCount: w.StashCount, Column: w.Column, Row: w.Row, Hidden: w.Hidden}
}

// SDRExpansion keeps active parameters absent when the decision is off.
type SDRExpansion struct {
	Active    bool     `json:"active"`
	Reason    string   `json:"reason"`
	Intensity *float64 `json:"intensity,omitempty"`
	Top       *float64 `json:"top,omitempty"`
	Decode    string   `json:"decode,omitempty"`
}

func expansion(s domain.SDRExpansionState) SDRExpansion {
	out := SDRExpansion{Active: s.Active, Reason: s.Reason}
	if s.Active {
		out.Intensity = &s.Intensity
		out.Top = &s.Top
		out.Decode = s.Decode
	}
	return out
}
