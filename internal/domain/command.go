package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// Caps is a set of control permissions.
type Caps uint8

const (
	CapRead Caps = 1 << iota
	CapAct
	CapInput
	CapCapture
	CapAdmin
	CapsAll = CapRead | CapAct | CapInput | CapCapture | CapAdmin
)

// CommandKind distinguishes command payloads.
type CommandKind uint8

const (
	CommandAction CommandKind = iota
	CommandActionArg
	CommandQuery
	CommandInject
	CommandShot
)

// String returns the protocol name of the command payload kind.
func (k CommandKind) String() string {
	switch k {
	case CommandAction:
		return "action"
	case CommandActionArg:
		return "action-arg"
	case CommandQuery:
		return "query"
	case CommandInject:
		return "inject"
	case CommandShot:
		return "shot"
	default:
		return fmt.Sprintf("CommandKind(%d)", k)
	}
}

// CommandSpec describes a command; Arg documents its payload, not its value.
type CommandSpec struct {
	Name, Arg, Summary string
	Kind               CommandKind
	Caps               Caps
}

var commandCatalog = []CommandSpec{
	{Name: "spawn-terminal", Summary: "Spawn terminal.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-column-left", Summary: "Focus column left.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-column-right", Summary: "Focus column right.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-window-up", Summary: "Focus window up.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-window-down", Summary: "Focus window down.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-column-left", Summary: "Move column left.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-column-right", Summary: "Move column right.", Kind: CommandAction, Caps: CapAct},
	{Name: "cycle-column-width", Summary: "Cycle column width.", Kind: CommandAction, Caps: CapAct},
	{Name: "maximize-column", Summary: "Maximize column.", Kind: CommandAction, Caps: CapAct},
	{Name: "toggle-fullscreen", Summary: "Toggle fullscreen.", Kind: CommandAction, Caps: CapAct},
	{Name: "toggle-window-stash", Summary: "Toggle window stash.", Kind: CommandAction, Caps: CapAct},
	{Name: "toggle-stash-visible", Summary: "Toggle stash visible.", Kind: CommandAction, Caps: CapAct},
	{Name: "toggle-overview", Summary: "Toggle overview.", Kind: CommandAction, Caps: CapAct},
	{Name: "close-window", Summary: "Close window.", Kind: CommandAction, Caps: CapAct},
	{Name: "quit", Summary: "Quit.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-workspace-prev", Summary: "Focus workspace prev.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-workspace-next", Summary: "Focus workspace next.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-column-to-workspace-prev", Summary: "Move column to workspace prev.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-column-to-workspace-next", Summary: "Move column to workspace next.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-window-to-workspace-prev", Summary: "Move window to workspace prev.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-window-to-workspace-next", Summary: "Move window to workspace next.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-monitor-left", Summary: "Focus monitor left.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-monitor-right", Summary: "Focus monitor right.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-monitor-up", Summary: "Focus monitor up.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-monitor-down", Summary: "Focus monitor down.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-to-monitor-left", Summary: "Move workspace to monitor left.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-to-monitor-right", Summary: "Move workspace to monitor right.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-to-monitor-up", Summary: "Move workspace to monitor up.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-to-monitor-down", Summary: "Move workspace to monitor down.", Kind: CommandAction, Caps: CapAct},
	{Name: "scale-up", Summary: "Scale up.", Kind: CommandAction, Caps: CapAct},
	{Name: "scale-down", Summary: "Scale down.", Kind: CommandAction, Caps: CapAct},
	{Name: "consume-or-expel-window-left", Summary: "Consume or expel window left.", Kind: CommandAction, Caps: CapAct},
	{Name: "consume-or-expel-window-right", Summary: "Consume or expel window right.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-window-up", Summary: "Move window up.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-window-down", Summary: "Move window down.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-prev", Summary: "Move workspace prev.", Kind: CommandAction, Caps: CapAct},
	{Name: "move-workspace-next", Summary: "Move workspace next.", Kind: CommandAction, Caps: CapAct},
	{Name: "toggle-floating", Summary: "Toggle floating.", Kind: CommandAction, Caps: CapAct},
	{Name: "switch-column-next", Summary: "Switch column next.", Kind: CommandAction, Caps: CapAct},
	{Name: "switch-column-prev", Summary: "Switch column prev.", Kind: CommandAction, Caps: CapAct},
	{Name: "hint", Summary: "Hint.", Kind: CommandAction, Caps: CapAct},
	{Name: "osd-pin", Summary: "Osd pin.", Kind: CommandAction, Caps: CapAct},
	{Name: "none", Summary: "None.", Kind: CommandAction, Caps: CapAct},
	{Name: "focus-workspace", Arg: "N", Summary: "Focus numbered workspace.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "move-column-to-workspace", Arg: "N", Summary: "Move column to numbered workspace.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "move-window-to-workspace", Arg: "N", Summary: "Move window to numbered workspace.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "workspace", Arg: "NAME", Summary: "Toggle named workspace.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "spawn", Arg: "COMMAND", Summary: "Run commands without a shell.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "stash", Arg: "COMMAND", Summary: "Run commands into the stash.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "set-column-width", Arg: "+N|-N", Summary: "Adjust column width in cells.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "set-window-height", Arg: "+N%|-N%", Summary: "Adjust window height in percent.", Kind: CommandActionArg, Caps: CapAct},
	{Name: "state", Arg: "", Summary: "Read current state.", Kind: CommandQuery, Caps: CapRead},
	{Name: "key", Arg: "KEY", Summary: "Inject a key.", Kind: CommandInject, Caps: CapInput},
	{Name: "type", Arg: "TEXT", Summary: "Inject text.", Kind: CommandInject, Caps: CapInput},
	{Name: "pointer", Arg: "POSITION", Summary: "Inject pointer input.", Kind: CommandInject, Caps: CapInput},
	{Name: "swipe", Arg: "GESTURE", Summary: "Inject a swipe.", Kind: CommandInject, Caps: CapInput},
	{Name: "shot", Arg: "", Summary: "Capture an output.", Kind: CommandShot, Caps: CapCapture},
}

// Commands returns an independent copy of the command catalog.
func Commands() []CommandSpec { return append([]CommandSpec(nil), commandCatalog...) }

// LookupCommand accepts exact catalog names or action-argument prefixes with
// a nonempty payload. Payload validation belongs to the action executor.
func LookupCommand(text string) (CommandSpec, bool) {
	for _, spec := range commandCatalog {
		if text == spec.Name {
			return spec, true
		}
		if spec.Kind == CommandActionArg {
			if arg, ok := strings.CutPrefix(text, spec.Name+" "); ok && strings.TrimSpace(arg) != "" {
				return spec, true
			}
		}
	}
	return CommandSpec{}, false
}

// WaitLevel identifies the completion boundary required by a caller.
type WaitLevel uint8

const (
	WaitCommitted WaitLevel = iota
	WaitSettled
	WaitPresented
	DefaultWait = WaitSettled
)

func (w WaitLevel) String() string {
	switch w {
	case WaitCommitted:
		return "committed"
	case WaitSettled:
		return "settled"
	case WaitPresented:
		return "presented"
	default:
		return fmt.Sprintf("WaitLevel(%d)", w)
	}
}

func ParseWaitLevel(s string) (WaitLevel, error) {
	switch s {
	case "committed":
		return WaitCommitted, nil
	case "settled":
		return WaitSettled, nil
	case "presented":
		return WaitPresented, nil
	default:
		return 0, fmt.Errorf("invalid wait level %q", s)
	}
}

// UntilKind identifies a state predicate. Idle is enforced by WaitSettled,
// rather than by a predicate over a snapshot.
type UntilKind uint8

const (
	UntilWindowMapped UntilKind = iota
	UntilFocused
	UntilUnmapped
)

type Until struct {
	Kind   UntilKind
	AppID  string
	Window WindowID
}

// ParseUntil parses window-mapped/focused app_id=APP or unmapped id=ID.
func ParseUntil(s string) (Until, error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return Until{}, fmt.Errorf("invalid until predicate %q", s)
	}
	key, value, ok := strings.Cut(fields[1], "=")
	if !ok || value == "" {
		return Until{}, fmt.Errorf("invalid until selector %q", fields[1])
	}
	switch fields[0] {
	case "window-mapped", "focused":
		if key == "app_id" {
			kind := UntilWindowMapped
			if fields[0] == "focused" {
				kind = UntilFocused
			}
			return Until{Kind: kind, AppID: value}, nil
		}
	case "unmapped":
		if key == "id" {
			id, err := strconv.ParseUint(value, 10, 64)
			if err == nil && id != 0 {
				return Until{Kind: UntilUnmapped, Window: WindowID(id)}, nil
			}
		}
	}
	return Until{}, fmt.Errorf("invalid until predicate %q", s)
}

func (u Until) Holds(s State) bool {
	switch u.Kind {
	case UntilWindowMapped:
		if u.AppID == "" {
			return false
		}
		for _, w := range s.Windows {
			if w.AppID == u.AppID {
				return true
			}
		}
	case UntilFocused:
		return u.AppID != "" && s.Window != nil && s.Window.AppID == u.AppID
	case UntilUnmapped:
		if u.Window == 0 {
			return false
		}
		for _, w := range s.Windows {
			if w.ID == u.Window {
				return false
			}
		}
		return true
	}
	return false
}

// ShotRequest describes an output capture at or after MinSeq.
type ShotRequest struct {
	Output string
	MinSeq uint64
	Raw    bool
}

// StateDiff describes mapped-window, focus and active-workspace changes.
// Window lists follow snapshot order; Workspaces follows after.Outputs order.
type StateDiff struct {
	Mapped, Unmapped   []WindowID
	FocusFrom, FocusTo WindowID
	FocusChanged       bool
	// Workspaces contains output:ws, using a configured name when present,
	// otherwise the active workspace number. Removed outputs are omitted.
	Workspaces []string
}

func DiffState(before, after State) StateDiff {
	var d StateDiff
	oldWindows := make(map[WindowID]bool, len(before.Windows))
	newWindows := make(map[WindowID]bool, len(after.Windows))
	for _, w := range before.Windows {
		oldWindows[w.ID] = true
	}
	for _, w := range after.Windows {
		newWindows[w.ID] = true
		if !oldWindows[w.ID] {
			d.Mapped = append(d.Mapped, w.ID)
		}
	}
	for _, w := range before.Windows {
		if !newWindows[w.ID] {
			d.Unmapped = append(d.Unmapped, w.ID)
		}
	}
	if before.Window != nil {
		d.FocusFrom = before.Window.ID
	}
	if after.Window != nil {
		d.FocusTo = after.Window.ID
	}
	d.FocusChanged = d.FocusFrom != d.FocusTo
	outputs := make(map[string]OutputState, len(before.Outputs))
	for _, o := range before.Outputs {
		outputs[o.Name] = o
	}
	for _, o := range after.Outputs {
		old, found := outputs[o.Name]
		if found && old.Active == o.Active && old.Workspace == o.Workspace && old.WorkspaceID == o.WorkspaceID {
			continue
		}
		ws := o.Workspace
		if ws == "" {
			ws = strconv.Itoa(o.Active)
		}
		d.Workspaces = append(d.Workspaces, o.Name+":"+ws)
	}
	return d
}
