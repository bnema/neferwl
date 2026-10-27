package core

import (
	"github.com/bnema/neferwl/internal/core/layout"
	workspacedomain "github.com/bnema/neferwl/internal/core/workspace"
)

// Core names the workspace vocabulary unqualified: Workspace, Action, Rect,
// the Action values and the cross-workspace operations read the same in every
// core file. This file is the one place that binds those names to
// internal/core/workspace. The import alias workspacedomain keeps the package
// name from clashing with the local variables called workspace.

// WindowID identifies a managed window.
type WindowID = workspacedomain.WindowID

// Rect is a logical-pixel rectangle.
type Rect = workspacedomain.Rect

// Placement describes a window and its presentation geometry.
type Placement = workspacedomain.Placement

// Workspace is the window, focus and presentation model of one workspace.
type Workspace = workspacedomain.Workspace

// Float is a floating window and its client size.
type Float = workspacedomain.Float

// Border is an output-local resize hit zone between columns or rows.
type Border = workspacedomain.Border

// Action is a bindable compositor action.
type Action = workspacedomain.Action

// Effect is what a workspace action asks of the monitor.
type Effect = workspacedomain.Effect

// DropSpot is an output-local drop target.
type DropSpot = workspacedomain.DropSpot

// DropKind says how a dragged tile joins its destination.
type DropKind = workspacedomain.DropKind

// OverviewRow is the monitor's provisional selection for one overview row.
type OverviewRow = workspacedomain.OverviewRow

// RowSnapshot is an overview row as first shown, restored on cancel.
type RowSnapshot = workspacedomain.RowSnapshot

// Sizes is a snapshot of column spans and row shares, restored on cancel.
type Sizes = workspacedomain.Sizes

// StackItem is one card of an overview row's stack.
type StackItem = workspacedomain.StackItem

// BorderColumn is workspace.BorderColumn.
const BorderColumn = workspacedomain.BorderColumn

// BorderRow is workspace.BorderRow.
const BorderRow = workspacedomain.BorderRow

// ActionSpawnTerminal is workspace.ActionSpawnTerminal.
const ActionSpawnTerminal = workspacedomain.ActionSpawnTerminal

// ActionFocusColumnLeft is workspace.ActionFocusColumnLeft.
const ActionFocusColumnLeft = workspacedomain.ActionFocusColumnLeft

// ActionFocusColumnRight is workspace.ActionFocusColumnRight.
const ActionFocusColumnRight = workspacedomain.ActionFocusColumnRight

// ActionFocusWindowUp is workspace.ActionFocusWindowUp.
const ActionFocusWindowUp = workspacedomain.ActionFocusWindowUp

// ActionFocusWindowDown is workspace.ActionFocusWindowDown.
const ActionFocusWindowDown = workspacedomain.ActionFocusWindowDown

// ActionMoveColumnLeft is workspace.ActionMoveColumnLeft.
const ActionMoveColumnLeft = workspacedomain.ActionMoveColumnLeft

// ActionMoveColumnRight is workspace.ActionMoveColumnRight.
const ActionMoveColumnRight = workspacedomain.ActionMoveColumnRight

// ActionCycleColumnWidth is workspace.ActionCycleColumnWidth.
const ActionCycleColumnWidth = workspacedomain.ActionCycleColumnWidth

// ActionMaximizeColumn is workspace.ActionMaximizeColumn.
const ActionMaximizeColumn = workspacedomain.ActionMaximizeColumn

// ActionToggleFullscreen is workspace.ActionToggleFullscreen.
const ActionToggleFullscreen = workspacedomain.ActionToggleFullscreen

// ActionToggleWindowStash is workspace.ActionToggleWindowStash.
const ActionToggleWindowStash = workspacedomain.ActionToggleWindowStash

// ActionToggleStashVisible is workspace.ActionToggleStashVisible.
const ActionToggleStashVisible = workspacedomain.ActionToggleStashVisible

// ActionToggleOverview is workspace.ActionToggleOverview.
const ActionToggleOverview = workspacedomain.ActionToggleOverview

// ActionCloseWindow is workspace.ActionCloseWindow.
const ActionCloseWindow = workspacedomain.ActionCloseWindow

// ActionQuit is workspace.ActionQuit.
const ActionQuit = workspacedomain.ActionQuit

// ActionFocusWorkspacePrev is workspace.ActionFocusWorkspacePrev.
const ActionFocusWorkspacePrev = workspacedomain.ActionFocusWorkspacePrev

// ActionFocusWorkspaceNext is workspace.ActionFocusWorkspaceNext.
const ActionFocusWorkspaceNext = workspacedomain.ActionFocusWorkspaceNext

// ActionMoveColumnToWorkspacePrev is workspace.ActionMoveColumnToWorkspacePrev.
const ActionMoveColumnToWorkspacePrev = workspacedomain.ActionMoveColumnToWorkspacePrev

// ActionMoveColumnToWorkspaceNext is workspace.ActionMoveColumnToWorkspaceNext.
const ActionMoveColumnToWorkspaceNext = workspacedomain.ActionMoveColumnToWorkspaceNext

// ActionMoveWindowToWorkspacePrev is workspace.ActionMoveWindowToWorkspacePrev.
const ActionMoveWindowToWorkspacePrev = workspacedomain.ActionMoveWindowToWorkspacePrev

// ActionMoveWindowToWorkspaceNext is workspace.ActionMoveWindowToWorkspaceNext.
const ActionMoveWindowToWorkspaceNext = workspacedomain.ActionMoveWindowToWorkspaceNext

// ActionFocusMonitorLeft is workspace.ActionFocusMonitorLeft.
const ActionFocusMonitorLeft = workspacedomain.ActionFocusMonitorLeft

// ActionFocusMonitorRight is workspace.ActionFocusMonitorRight.
const ActionFocusMonitorRight = workspacedomain.ActionFocusMonitorRight

// ActionFocusMonitorUp is workspace.ActionFocusMonitorUp.
const ActionFocusMonitorUp = workspacedomain.ActionFocusMonitorUp

// ActionFocusMonitorDown is workspace.ActionFocusMonitorDown.
const ActionFocusMonitorDown = workspacedomain.ActionFocusMonitorDown

// ActionMoveWorkspaceLeft is workspace.ActionMoveWorkspaceLeft.
const ActionMoveWorkspaceLeft = workspacedomain.ActionMoveWorkspaceLeft

// ActionMoveWorkspaceRight is workspace.ActionMoveWorkspaceRight.
const ActionMoveWorkspaceRight = workspacedomain.ActionMoveWorkspaceRight

// ActionMoveWorkspaceToMonitorUp is workspace.ActionMoveWorkspaceToMonitorUp.
const ActionMoveWorkspaceToMonitorUp = workspacedomain.ActionMoveWorkspaceToMonitorUp

// ActionMoveWorkspaceToMonitorDown is workspace.ActionMoveWorkspaceToMonitorDown.
const ActionMoveWorkspaceToMonitorDown = workspacedomain.ActionMoveWorkspaceToMonitorDown

// ActionScaleUp is workspace.ActionScaleUp.
const ActionScaleUp = workspacedomain.ActionScaleUp

// ActionScaleDown is workspace.ActionScaleDown.
const ActionScaleDown = workspacedomain.ActionScaleDown

// ActionConsumeOrExpelLeft is workspace.ActionConsumeOrExpelLeft.
const ActionConsumeOrExpelLeft = workspacedomain.ActionConsumeOrExpelLeft

// ActionConsumeOrExpelRight is workspace.ActionConsumeOrExpelRight.
const ActionConsumeOrExpelRight = workspacedomain.ActionConsumeOrExpelRight

// ActionMoveWindowUp is workspace.ActionMoveWindowUp.
const ActionMoveWindowUp = workspacedomain.ActionMoveWindowUp

// ActionMoveWindowDown is workspace.ActionMoveWindowDown.
const ActionMoveWindowDown = workspacedomain.ActionMoveWindowDown

// ActionMoveWorkspacePrev is workspace.ActionMoveWorkspacePrev.
const ActionMoveWorkspacePrev = workspacedomain.ActionMoveWorkspacePrev

// ActionMoveWorkspaceNext is workspace.ActionMoveWorkspaceNext.
const ActionMoveWorkspaceNext = workspacedomain.ActionMoveWorkspaceNext

// ActionToggleFloating is workspace.ActionToggleFloating.
const ActionToggleFloating = workspacedomain.ActionToggleFloating

// ActionSwitchColumnNext is workspace.ActionSwitchColumnNext.
const ActionSwitchColumnNext = workspacedomain.ActionSwitchColumnNext

// ActionSwitchColumnPrev is workspace.ActionSwitchColumnPrev.
const ActionSwitchColumnPrev = workspacedomain.ActionSwitchColumnPrev

// ActionHint is workspace.ActionHint.
const ActionHint = workspacedomain.ActionHint

// ActionOSDPin is workspace.ActionOSDPin.
const ActionOSDPin = workspacedomain.ActionOSDPin

// FocusWorkspace is workspace.FocusWorkspace.
const FocusWorkspace = workspacedomain.FocusWorkspace

// MoveColumnToWorkspace is workspace.MoveColumnToWorkspace.
const MoveColumnToWorkspace = workspacedomain.MoveColumnToWorkspace

// DropNone is workspace.DropNone.
const DropNone = workspacedomain.DropNone

// EdgeAlways is workspace.EdgeAlways.
const EdgeAlways = workspacedomain.EdgeAlways

// EdgeIfStuck is workspace.EdgeIfStuck.
const EdgeIfStuck = workspacedomain.EdgeIfStuck

// EdgeFirst is workspace.EdgeFirst.
const EdgeFirst = workspacedomain.EdgeFirst

// StackFloat is workspace.StackFloat.
const StackFloat = workspacedomain.StackFloat

// StackColumns is workspace.StackColumns.
const StackColumns = workspacedomain.StackColumns

// StackColumn is workspace.StackColumn.
const StackColumn = workspacedomain.StackColumn

// ResizeArg calls workspace.ResizeArg.
func ResizeArg(a Action) (axis, step int, ok bool) { return workspacedomain.ResizeArg(a) }

// WorkspaceArg calls workspace.WorkspaceArg.
func WorkspaceArg(a Action) (n int, op workspacedomain.WorkspaceOp, ok bool) {
	return workspacedomain.WorkspaceArg(a)
}

// NamedArg calls workspace.NamedArg.
func NamedArg(a Action) (string, bool) { return workspacedomain.NamedArg(a) }

// SpawnCommands calls workspace.SpawnCommands.
func SpawnCommands(a Action) ([][]string, bool) { return workspacedomain.SpawnCommands(a) }

// StashCommands calls workspace.StashCommands.
func StashCommands(a Action) ([][]string, bool) { return workspacedomain.StashCommands(a) }

// MoveFullscreen calls workspace.MoveFullscreen.
func MoveFullscreen(from, to *Workspace, at int) bool {
	return workspacedomain.MoveFullscreen(from, to, at)
}

// MoveColumnTo calls workspace.MoveColumnTo.
func MoveColumnTo(from, to *Workspace, at int) bool {
	return workspacedomain.MoveColumnTo(from, to, at)
}

// MoveFocused calls workspace.MoveFocused.
func MoveFocused(from, to *Workspace, column bool) bool {
	return workspacedomain.MoveFocused(from, to, column)
}

// Expel calls workspace.Expel.
func Expel(from, to *Workspace, dir int) bool { return workspacedomain.Expel(from, to, dir) }

// Merge calls workspace.Merge.
func Merge(dst, src *Workspace) { workspacedomain.Merge(dst, src) }

// NewFrom calls workspace.NewFrom.
func NewFrom(t *Workspace, id uint64) *Workspace { return workspacedomain.NewFrom(t, id) }

// Drop calls workspace.Drop.
func Drop(src, w *Workspace, id WindowID, t DropSpot) { workspacedomain.Drop(src, w, id, t) }

// NeighborSlot calls workspace.NeighborSlot.
func NeighborSlot(axis layout.Axis, u, cs Rect, w *Workspace, before bool, spacing int) (slot Rect, gapAt int) {
	return workspacedomain.NeighborSlot(axis, u, cs, w, before, spacing)
}
