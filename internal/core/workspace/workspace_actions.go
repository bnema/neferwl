package workspace

import (
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

// Action describes the workspace action contract.
type Action string

const (
	// ActionSpawnTerminal is the ActionSpawnTerminal action value.
	ActionSpawnTerminal Action = "spawn-terminal"
	// ActionFocusColumnLeft is the ActionFocusColumnLeft action value.
	ActionFocusColumnLeft Action = "focus-column-left"
	// ActionFocusColumnRight is the ActionFocusColumnRight action value.
	ActionFocusColumnRight Action = "focus-column-right"
	// ActionFocusWindowUp is the ActionFocusWindowUp action value.
	ActionFocusWindowUp Action = "focus-window-up"
	// ActionFocusWindowDown is the ActionFocusWindowDown action value.
	ActionFocusWindowDown Action = "focus-window-down"
	// ActionMoveColumnLeft is the ActionMoveColumnLeft action value.
	ActionMoveColumnLeft Action = "move-column-left"
	// ActionMoveColumnRight is the ActionMoveColumnRight action value.
	ActionMoveColumnRight Action = "move-column-right"
	// ActionCycleColumnWidth is the ActionCycleColumnWidth action value.
	ActionCycleColumnWidth Action = "cycle-column-width"
	// ActionMaximizeColumn is the ActionMaximizeColumn action value.
	ActionMaximizeColumn Action = "maximize-column"
	// ActionToggleFullscreen is the ActionToggleFullscreen action value.
	ActionToggleFullscreen Action = "toggle-fullscreen"
	// ActionToggleWindowStash is the ActionToggleWindowStash action value.
	ActionToggleWindowStash Action = "toggle-window-stash"
	// ActionToggleStashVisible is the ActionToggleStashVisible action value.
	ActionToggleStashVisible Action = "toggle-stash-visible"
	// ActionToggleOverview is the ActionToggleOverview action value.
	ActionToggleOverview Action = "toggle-overview"
	// ActionCloseWindow is the ActionCloseWindow action value.
	ActionCloseWindow Action = "close-window"
	// ActionQuit is the ActionQuit action value.
	ActionQuit Action = "quit"
	// Workspaces stack vertically; prev/next stop at the ends.
	ActionFocusWorkspacePrev Action = "focus-workspace-prev"
	// ActionFocusWorkspaceNext is the ActionFocusWorkspaceNext action value.
	ActionFocusWorkspaceNext Action = "focus-workspace-next"
	// Move the focused column, or only the focused window, to the previous
	// or next workspace.
	ActionMoveColumnToWorkspacePrev Action = "move-column-to-workspace-prev"
	// ActionMoveColumnToWorkspaceNext is the ActionMoveColumnToWorkspaceNext action value.
	ActionMoveColumnToWorkspaceNext Action = "move-column-to-workspace-next"
	// ActionMoveWindowToWorkspacePrev is the ActionMoveWindowToWorkspacePrev action value.
	ActionMoveWindowToWorkspacePrev Action = "move-window-to-workspace-prev"
	// ActionMoveWindowToWorkspaceNext is the ActionMoveWindowToWorkspaceNext action value.
	ActionMoveWindowToWorkspaceNext Action = "move-window-to-workspace-next"
	// Monitors act on the neighbor in that direction of the global layout
	// (ADR 011).
	ActionFocusMonitorLeft Action = "focus-monitor-left"
	// ActionFocusMonitorRight is the ActionFocusMonitorRight action value.
	ActionFocusMonitorRight Action = "focus-monitor-right"
	// ActionFocusMonitorUp is the ActionFocusMonitorUp action value.
	ActionFocusMonitorUp Action = "focus-monitor-up"
	// ActionFocusMonitorDown is the ActionFocusMonitorDown action value.
	ActionFocusMonitorDown Action = "focus-monitor-down"
	// ActionMoveWorkspaceLeft is the ActionMoveWorkspaceLeft action value.
	ActionMoveWorkspaceLeft Action = "move-workspace-to-monitor-left"
	// ActionMoveWorkspaceRight is the ActionMoveWorkspaceRight action value.
	ActionMoveWorkspaceRight Action = "move-workspace-to-monitor-right"
	// ActionMoveWorkspaceToMonitorUp is the ActionMoveWorkspaceToMonitorUp action value.
	ActionMoveWorkspaceToMonitorUp Action = "move-workspace-to-monitor-up"
	// ActionMoveWorkspaceToMonitorDown is the ActionMoveWorkspaceToMonitorDown action value.
	ActionMoveWorkspaceToMonitorDown Action = "move-workspace-to-monitor-down"
	// Scale steps through the clean scales of the output (see CleanScales).
	ActionScaleUp Action = "scale-up"
	// ActionScaleDown is the ActionScaleDown action value.
	ActionScaleDown Action = "scale-down"
	// Consume or expel the focused window (ADR 016).
	ActionConsumeOrExpelLeft Action = "consume-or-expel-window-left"
	// ActionConsumeOrExpelRight is the ActionConsumeOrExpelRight action value.
	ActionConsumeOrExpelRight Action = "consume-or-expel-window-right"
	// Move the focused window up or down inside its column.
	ActionMoveWindowUp Action = "move-window-up"
	// ActionMoveWindowDown is the ActionMoveWindowDown action value.
	ActionMoveWindowDown Action = "move-window-down"
	// Swap the active numbered workspace with its neighbor.
	ActionMoveWorkspacePrev Action = "move-workspace-prev"
	// ActionMoveWorkspaceNext is the ActionMoveWorkspaceNext action value.
	ActionMoveWorkspaceNext Action = "move-workspace-next"
	// Turn the focused tile into a free floating window, or back.
	ActionToggleFloating Action = "toggle-floating"
	// Switch to the column used before this one, further back while the
	// command key is held (core/switcher.go).
	ActionSwitchColumnNext Action = "switch-column-next"
	// ActionSwitchColumnPrev is the ActionSwitchColumnPrev action value.
	ActionSwitchColumnPrev Action = "switch-column-prev"
	// Show keyboard hint labels over the links and buttons of the focused
	// output (hint.go).
	ActionHint Action = "hint"
	// ActionOSDPin toggles the pinned window info overlay.
	ActionOSDPin Action = "osd-pin"
)

// Resize axes of ResizeArg.
const (
	// ResizeWidth is the ResizeWidth action value.
	ResizeWidth = 0
	// ResizeHeight is the ResizeHeight action value.
	ResizeHeight = 1
)

var resizePrefixes = [...]string{
	ResizeWidth:  "set-column-width ",
	ResizeHeight: "set-window-height ",
}

// ResizeArg parses "set-column-width +N" (axis ResizeWidth) and
// "set-window-height -N%" (axis ResizeHeight). The sign is required and the
// unit depends on the axis: step is in cells for a width (N from 1 to 16,
// no %), in percent for a height (N from 1 to 100, with %).
func ResizeArg(a Action) (axis, step int, ok bool) {
	for i, prefix := range resizePrefixes {
		rest, found := strings.CutPrefix(string(a), prefix)
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		if i == ResizeWidth {
			if p, ok := parseResizeStep(rest, layout.MaxCells); ok {
				return i, p, true
			}
			return 0, 0, false
		}
		if num, found := strings.CutSuffix(rest, "%"); found {
			if p, ok := parseResizeStep(num, 100); ok {
				return i, p, true
			}
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// parseResizeStep parses "+N" or "-N" with N from 1 to limit.
func parseResizeStep(s string, limit int) (int, bool) {
	if len(s) < 2 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > limit || s[1] == '+' || s[1] == '-' {
		return 0, false
	}
	if s[0] == '-' {
		n = -n
	}
	return n, true
}

// WorkspaceOp is what a numbered workspace action does.
type WorkspaceOp int

const (
	// FocusWorkspace is the FocusWorkspace action value.
	FocusWorkspace WorkspaceOp = iota
	// MoveColumnToWorkspace is the MoveColumnToWorkspace action value.
	MoveColumnToWorkspace
	// MoveWindowToWorkspace is the MoveWindowToWorkspace action value.
	MoveWindowToWorkspace
)

var workspacePrefixes = [...]string{
	FocusWorkspace:        "focus-workspace ",
	MoveColumnToWorkspace: "move-column-to-workspace ",
	MoveWindowToWorkspace: "move-window-to-workspace ",
}

// WorkspaceArg parses "focus-workspace N", "move-column-to-workspace N" and
// "move-window-to-workspace N" (N from 1).
func WorkspaceArg(a Action) (n int, op WorkspaceOp, ok bool) {
	for i, prefix := range workspacePrefixes {
		rest, found := strings.CutPrefix(string(a), prefix)
		if !found {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n < 1 || n > 99 {
			return 0, 0, false
		}
		return n, WorkspaceOp(i), true
	}
	return 0, 0, false
}

// namedPrefix toggles a named workspace: "workspace dev".
const namedPrefix = "workspace "

// NamedArg returns the name of a "workspace <name>" action.
func NamedArg(a Action) (string, bool) {
	rest, ok := strings.CutPrefix(string(a), namedPrefix)
	name := strings.TrimSpace(rest)
	return name, ok && name != "" && !strings.ContainsAny(name, " \t")
}

// spawnPrefix starts a bind action that runs commands: "spawn fuzzel --flag".
// They are split by domain.SplitCommands; there is no shell.
const spawnPrefix = "spawn "

// SpawnCommands returns the commands of a "spawn <cmd>[; <cmd>...]" action.
func SpawnCommands(a Action) ([][]string, bool) { return prefixedCommands(a, spawnPrefix) }

// stashPrefix starts a bind action that runs commands and opens their
// windows in the stash: "stash firefox".
const stashPrefix = "stash "

// StashCommands returns the commands of a "stash <cmd>[; <cmd>...]" action.
func StashCommands(a Action) ([][]string, bool) { return prefixedCommands(a, stashPrefix) }

func prefixedCommands(a Action, prefix string) ([][]string, bool) {
	rest, ok := strings.CutPrefix(string(a), prefix)
	if !ok {
		return nil, false
	}
	commands := domain.SplitCommands(rest)
	return commands, len(commands) > 0
}

// Effect describes the workspace action contract.
type Effect struct {
	// Spawn describes the workspace action contract.
	Spawn bool
	// Commands describes the workspace action contract.
	Commands [][]string // commands to run; nil means the configured terminal
	// Stash sends the spawned windows to the stash of the workspace the
	// action applied to.
	Stash bool
	// Close describes the workspace action contract.
	Close WindowID
	// Quit describes the workspace action contract.
	Quit bool
}

// Apply runs a bind action that only touches this workspace.
func (w *Workspace) Apply(a Action) Effect {
	if commands, ok := SpawnCommands(a); ok {
		return Effect{Spawn: true, Commands: commands}
	}
	if commands, ok := StashCommands(a); ok {
		return Effect{Spawn: true, Stash: true, Commands: commands}
	}
	if w.ApplyFree(a) {
		return Effect{}
	}
	if axis, step, ok := ResizeArg(a); ok {
		if axis == ResizeWidth {
			for range max(step, -step) {
				if step > 0 {
					w.GrowColumn(false)
				} else {
					w.ShrinkColumn()
				}
			}
		} else {
			w.ResizeRow(step)
		}
		return Effect{}
	}
	switch a {
	case ActionSpawnTerminal:
		return Effect{Spawn: true}
	case ActionFocusColumnLeft:
		w.FocusColumn(-1)
	case ActionFocusColumnRight:
		w.FocusColumn(1)
	case ActionFocusWindowUp:
		w.FocusWindow(-1)
	case ActionFocusWindowDown:
		w.FocusWindow(1)
	case ActionMoveColumnLeft:
		w.MoveColumn(-1)
	case ActionMoveColumnRight:
		w.MoveColumn(1)
	case ActionMoveWindowUp:
		w.MoveWindow(-1)
	case ActionMoveWindowDown:
		w.MoveWindow(1)
	case ActionCycleColumnWidth:
		w.GrowColumn(true)
	case ActionMaximizeColumn:
		w.ToggleFullWidth()
	case ActionToggleFullscreen:
		w.ToggleFullscreen()
	case ActionToggleWindowStash:
		w.ToggleWindowStash()
	case ActionToggleStashVisible:
		w.ToggleStashVisible()
	case ActionToggleFloating:
		w.ToggleFloating()
	case ActionCloseWindow:
		id, _ := w.Focused()
		return Effect{Close: id}
	case ActionQuit:
		return Effect{Quit: true}
	}
	return Effect{}
}
