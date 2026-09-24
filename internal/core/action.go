package core

import "strings"

type Action string

const (
	ActionSpawnTerminal    Action = "spawn-terminal"
	ActionFocusColumnLeft  Action = "focus-column-left"
	ActionFocusColumnRight Action = "focus-column-right"
	ActionFocusWindowUp    Action = "focus-window-up"
	ActionFocusWindowDown  Action = "focus-window-down"
	ActionMoveColumnLeft   Action = "move-column-left"
	ActionMoveColumnRight  Action = "move-column-right"
	ActionCycleColumnWidth Action = "cycle-column-width"
	ActionToggleFullscreen Action = "toggle-fullscreen"
	ActionCloseWindow      Action = "close-window"
	ActionQuit             Action = "quit"
)

// spawnPrefix starts a bind action that runs a command: "spawn fuzzel --flag".
// Arguments are split on whitespace; there is no shell (use "spawn sh -c ...").
const spawnPrefix = "spawn "

// SpawnArgv returns the command of a "spawn <cmd>" action.
func SpawnArgv(a Action) ([]string, bool) {
	rest, ok := strings.CutPrefix(string(a), spawnPrefix)
	argv := strings.Fields(rest)
	return argv, ok && len(argv) > 0
}

type Effect struct {
	Spawn bool
	Argv  []string // command to run; nil means the configured terminal
	Close WindowID
	Quit  bool
}

func (w *Workspace) Apply(a Action) Effect {
	if argv, ok := SpawnArgv(a); ok {
		return Effect{Spawn: true, Argv: argv}
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
	case ActionCycleColumnWidth:
		w.CycleWidth()
	case ActionToggleFullscreen:
		w.ToggleFullscreen()
	case ActionCloseWindow:
		id, _ := w.Focused()
		return Effect{Close: id}
	case ActionQuit:
		return Effect{Quit: true}
	}
	return Effect{}
}
