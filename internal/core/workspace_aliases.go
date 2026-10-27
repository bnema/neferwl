package core

import (
	"github.com/bnema/neferwl/internal/core/layout"
	workspacedomain "github.com/bnema/neferwl/internal/core/workspace"
)

// WindowID retains core's shared window identity value contract.
type WindowID = workspacedomain.WindowID

// Rect retains core's logical geometry value contract.
type Rect = workspacedomain.Rect

// Placement retains core's scene-facing geometry contract.
type Placement = workspacedomain.Placement

// Workspace preserves core's existing caller contract while workspace owns the model.
type Workspace = workspacedomain.Workspace

// Float preserves core's existing caller contract while workspace owns the model.
type Float = workspacedomain.Float

// BorderKind preserves core's existing caller contract while workspace owns the model.
type BorderKind = workspacedomain.BorderKind

// Border preserves core's existing caller contract while workspace owns the model.
type Border = workspacedomain.Border

// Action preserves core's existing caller contract while workspace owns the model.
type Action = workspacedomain.Action

// WorkspaceOp preserves core's existing caller contract while workspace owns the model.
type WorkspaceOp = workspacedomain.WorkspaceOp

// Effect preserves core's existing caller contract while workspace owns the model.
type Effect = workspacedomain.Effect

// DropSpot preserves core's existing caller contract while workspace owns the model.
type DropSpot = workspacedomain.DropSpot

// DropKind preserves core's existing caller contract while workspace owns the model.
type DropKind = workspacedomain.DropKind

// EdgeKind preserves core's existing caller contract while workspace owns the model.
type EdgeKind = workspacedomain.EdgeKind

// OverviewRow preserves core's existing caller contract while workspace owns the model.
type OverviewRow = workspacedomain.OverviewRow

// RowSnapshot preserves core's existing caller contract while workspace owns the model.
type RowSnapshot = workspacedomain.RowSnapshot

// StackKind preserves core's existing caller contract while workspace owns the model.
type StackKind = workspacedomain.StackKind

// StackItem preserves core's existing caller contract while workspace owns the model.
type StackItem = workspacedomain.StackItem

// BorderColumn shares the model value with core dispatch and existing callers.
const BorderColumn = workspacedomain.BorderColumn

// BorderRow shares the model value with core dispatch and existing callers.
const BorderRow = workspacedomain.BorderRow

// ActionSpawnTerminal shares the model value with core dispatch and existing callers.
const ActionSpawnTerminal = workspacedomain.ActionSpawnTerminal

// ActionFocusColumnLeft shares the model value with core dispatch and existing callers.
const ActionFocusColumnLeft = workspacedomain.ActionFocusColumnLeft

// ActionFocusColumnRight shares the model value with core dispatch and existing callers.
const ActionFocusColumnRight = workspacedomain.ActionFocusColumnRight

// ActionFocusWindowUp shares the model value with core dispatch and existing callers.
const ActionFocusWindowUp = workspacedomain.ActionFocusWindowUp

// ActionFocusWindowDown shares the model value with core dispatch and existing callers.
const ActionFocusWindowDown = workspacedomain.ActionFocusWindowDown

// ActionMoveColumnLeft shares the model value with core dispatch and existing callers.
const ActionMoveColumnLeft = workspacedomain.ActionMoveColumnLeft

// ActionMoveColumnRight shares the model value with core dispatch and existing callers.
const ActionMoveColumnRight = workspacedomain.ActionMoveColumnRight

// ActionCycleColumnWidth shares the model value with core dispatch and existing callers.
const ActionCycleColumnWidth = workspacedomain.ActionCycleColumnWidth

// ActionMaximizeColumn shares the model value with core dispatch and existing callers.
const ActionMaximizeColumn = workspacedomain.ActionMaximizeColumn

// ActionToggleFullscreen shares the model value with core dispatch and existing callers.
const ActionToggleFullscreen = workspacedomain.ActionToggleFullscreen

// ActionToggleWindowStash shares the model value with core dispatch and existing callers.
const ActionToggleWindowStash = workspacedomain.ActionToggleWindowStash

// ActionToggleStashVisible shares the model value with core dispatch and existing callers.
const ActionToggleStashVisible = workspacedomain.ActionToggleStashVisible

// ActionToggleOverview shares the model value with core dispatch and existing callers.
const ActionToggleOverview = workspacedomain.ActionToggleOverview

// ActionCloseWindow shares the model value with core dispatch and existing callers.
const ActionCloseWindow = workspacedomain.ActionCloseWindow

// ActionQuit shares the model value with core dispatch and existing callers.
const ActionQuit = workspacedomain.ActionQuit

// ActionFocusWorkspacePrev shares the model value with core dispatch and existing callers.
const ActionFocusWorkspacePrev = workspacedomain.ActionFocusWorkspacePrev

// ActionFocusWorkspaceNext shares the model value with core dispatch and existing callers.
const ActionFocusWorkspaceNext = workspacedomain.ActionFocusWorkspaceNext

// ActionMoveColumnToWorkspacePrev shares the model value with core dispatch and existing callers.
const ActionMoveColumnToWorkspacePrev = workspacedomain.ActionMoveColumnToWorkspacePrev

// ActionMoveColumnToWorkspaceNext shares the model value with core dispatch and existing callers.
const ActionMoveColumnToWorkspaceNext = workspacedomain.ActionMoveColumnToWorkspaceNext

// ActionMoveWindowToWorkspacePrev shares the model value with core dispatch and existing callers.
const ActionMoveWindowToWorkspacePrev = workspacedomain.ActionMoveWindowToWorkspacePrev

// ActionMoveWindowToWorkspaceNext shares the model value with core dispatch and existing callers.
const ActionMoveWindowToWorkspaceNext = workspacedomain.ActionMoveWindowToWorkspaceNext

// ActionFocusMonitorLeft shares the model value with core dispatch and existing callers.
const ActionFocusMonitorLeft = workspacedomain.ActionFocusMonitorLeft

// ActionFocusMonitorRight shares the model value with core dispatch and existing callers.
const ActionFocusMonitorRight = workspacedomain.ActionFocusMonitorRight

// ActionFocusMonitorUp shares the model value with core dispatch and existing callers.
const ActionFocusMonitorUp = workspacedomain.ActionFocusMonitorUp

// ActionFocusMonitorDown shares the model value with core dispatch and existing callers.
const ActionFocusMonitorDown = workspacedomain.ActionFocusMonitorDown

// ActionMoveWorkspaceLeft shares the model value with core dispatch and existing callers.
const ActionMoveWorkspaceLeft = workspacedomain.ActionMoveWorkspaceLeft

// ActionMoveWorkspaceRight shares the model value with core dispatch and existing callers.
const ActionMoveWorkspaceRight = workspacedomain.ActionMoveWorkspaceRight

// ActionMoveWorkspaceToMonitorUp shares the model value with core dispatch and existing callers.
const ActionMoveWorkspaceToMonitorUp = workspacedomain.ActionMoveWorkspaceToMonitorUp

// ActionMoveWorkspaceToMonitorDown shares the model value with core dispatch and existing callers.
const ActionMoveWorkspaceToMonitorDown = workspacedomain.ActionMoveWorkspaceToMonitorDown

// ActionScaleUp shares the model value with core dispatch and existing callers.
const ActionScaleUp = workspacedomain.ActionScaleUp

// ActionScaleDown shares the model value with core dispatch and existing callers.
const ActionScaleDown = workspacedomain.ActionScaleDown

// ActionConsumeOrExpelLeft shares the model value with core dispatch and existing callers.
const ActionConsumeOrExpelLeft = workspacedomain.ActionConsumeOrExpelLeft

// ActionConsumeOrExpelRight shares the model value with core dispatch and existing callers.
const ActionConsumeOrExpelRight = workspacedomain.ActionConsumeOrExpelRight

// ActionMoveWindowUp shares the model value with core dispatch and existing callers.
const ActionMoveWindowUp = workspacedomain.ActionMoveWindowUp

// ActionMoveWindowDown shares the model value with core dispatch and existing callers.
const ActionMoveWindowDown = workspacedomain.ActionMoveWindowDown

// ActionMoveWorkspacePrev shares the model value with core dispatch and existing callers.
const ActionMoveWorkspacePrev = workspacedomain.ActionMoveWorkspacePrev

// ActionMoveWorkspaceNext shares the model value with core dispatch and existing callers.
const ActionMoveWorkspaceNext = workspacedomain.ActionMoveWorkspaceNext

// ActionToggleFloating shares the model value with core dispatch and existing callers.
const ActionToggleFloating = workspacedomain.ActionToggleFloating

// ActionSwitchColumnNext shares the model value with core dispatch and existing callers.
const ActionSwitchColumnNext = workspacedomain.ActionSwitchColumnNext

// ActionSwitchColumnPrev shares the model value with core dispatch and existing callers.
const ActionSwitchColumnPrev = workspacedomain.ActionSwitchColumnPrev

// ActionHint shares the model value with core dispatch and existing callers.
const ActionHint = workspacedomain.ActionHint

// ActionOSDPin shares the model value with core dispatch and existing callers.
const ActionOSDPin = workspacedomain.ActionOSDPin

// ResizeWidth shares the model value with core dispatch and existing callers.
const ResizeWidth = workspacedomain.ResizeWidth

// ResizeHeight shares the model value with core dispatch and existing callers.
const ResizeHeight = workspacedomain.ResizeHeight

// FocusWorkspace shares the model value with core dispatch and existing callers.
const FocusWorkspace = workspacedomain.FocusWorkspace

// MoveColumnToWorkspace shares the model value with core dispatch and existing callers.
const MoveColumnToWorkspace = workspacedomain.MoveColumnToWorkspace

// MoveWindowToWorkspace shares the model value with core dispatch and existing callers.
const MoveWindowToWorkspace = workspacedomain.MoveWindowToWorkspace

// DropNone shares the model value with core dispatch and existing callers.
const DropNone = workspacedomain.DropNone

// DropAbove shares the model value with core dispatch and existing callers.
const DropAbove = workspacedomain.DropAbove

// DropBelow shares the model value with core dispatch and existing callers.
const DropBelow = workspacedomain.DropBelow

// DropSwap shares the model value with core dispatch and existing callers.
const DropSwap = workspacedomain.DropSwap

// DropInsert shares the model value with core dispatch and existing callers.
const DropInsert = workspacedomain.DropInsert

// EdgeNone shares the model value with core dispatch and existing callers.
const EdgeNone = workspacedomain.EdgeNone

// EdgeAlways shares the model value with core dispatch and existing callers.
const EdgeAlways = workspacedomain.EdgeAlways

// EdgeIfStuck shares the model value with core dispatch and existing callers.
const EdgeIfStuck = workspacedomain.EdgeIfStuck

// EdgeFirst shares the model value with core dispatch and existing callers.
const EdgeFirst = workspacedomain.EdgeFirst

// StackFloat shares the model value with core dispatch and existing callers.
const StackFloat = workspacedomain.StackFloat

// StackColumns shares the model value with core dispatch and existing callers.
const StackColumns = workspacedomain.StackColumns

// StackColumn shares the model value with core dispatch and existing callers.
const StackColumn = workspacedomain.StackColumn

// ResizeArg forwards the model operation without changing existing core call sites.
func ResizeArg(a Action) (axis, step int, ok bool) { return workspacedomain.ResizeArg(a) }

// WorkspaceArg forwards the model operation without changing existing core call sites.
func WorkspaceArg(a Action) (n int, op WorkspaceOp, ok bool) { return workspacedomain.WorkspaceArg(a) }

// NamedArg forwards the model operation without changing existing core call sites.
func NamedArg(a Action) (string, bool) { return workspacedomain.NamedArg(a) }

// SpawnCommands forwards the model operation without changing existing core call sites.
func SpawnCommands(a Action) ([][]string, bool) { return workspacedomain.SpawnCommands(a) }

// StashCommands forwards the model operation without changing existing core call sites.
func StashCommands(a Action) ([][]string, bool) { return workspacedomain.StashCommands(a) }

// MoveFullscreen forwards the model operation without changing existing core call sites.
func MoveFullscreen(from, to *Workspace, at int) { workspacedomain.MoveFullscreen(from, to, at) }

// MoveFocused forwards the model operation without changing existing core call sites.
func MoveFocused(from, to *Workspace, column bool, at int) bool {
	return workspacedomain.MoveFocused(from, to, column, at)
}

// Expel forwards the model operation without changing existing core call sites.
func Expel(from, to *Workspace, dir int) bool { return workspacedomain.Expel(from, to, dir) }

// Merge forwards the model operation without changing existing core call sites.
func Merge(dst, src *Workspace) { workspacedomain.Merge(dst, src) }

// NewFrom forwards the model operation without changing existing core call sites.
func NewFrom(t *Workspace, id uint64) *Workspace { return workspacedomain.NewFrom(t, id) }

// Drop forwards the model operation without changing existing core call sites.
func Drop(src, w *Workspace, id WindowID, t DropSpot) { workspacedomain.Drop(src, w, id, t) }

// NeighborSlot forwards the model operation without changing existing core call sites.
func NeighborSlot(axis layout.Axis, u, cs Rect, w *Workspace, before bool, spacing int) (slot Rect, gapAt int) {
	return workspacedomain.NeighborSlot(axis, u, cs, w, before, spacing)
}
