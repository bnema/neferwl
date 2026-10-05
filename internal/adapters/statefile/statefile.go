// Package statefile publishes core's state as JSON for scripts, and reads it
// back for the `neferwl state` command. The JSON format is defined here,
// not in ports: it is a public interface scripts depend on.
package statefile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// Env names the state file in the environment of processes neferwl starts.
const Env = "NEFERWL_STATE"

// Path is the state file of the session on a wayland socket: one per
// session, so nested or test instances never overwrite each other.
func Path(runtimeDir, socket string) (string, error) {
	socket = filepath.Base(socket)
	if runtimeDir == "" || socket == "" || socket == "." || socket == "/" {
		return "", errors.New("XDG_RUNTIME_DIR and WAYLAND_DISPLAY must be set")
	}
	return filepath.Join(runtimeDir, "neferwl", socket+".json"), nil
}

// State is the file format.
type State struct {
	Output  string   `json:"output"`
	Outputs []Output `json:"outputs"`
	Window  *Window  `json:"window"`
	Windows []Window `json:"windows"`
}

// Output is one output: its active numbered workspace (0 while a hidden one
// is shown), how many numbered workspaces it has, and the name and ID of the
// workspace on screen. WorkspaceID is the same string ext_workspace_handle_v1
// sends as its id (package workspaceid): "name:<name>" for a configured
// workspace, else "<launch prefix>-<core ID>".
type Output struct {
	Name        string `json:"name"`
	Active      int    `json:"active"`
	Count       int    `json:"count"`
	Workspace   string `json:"workspace"`
	WorkspaceID string `json:"workspace_id"`
}

// Window is one mapped window.
type Window struct {
	ID        uint64 `json:"id"`
	AppID     string `json:"app_id"`
	PID       int    `json:"pid"`
	Output    string `json:"output"`
	Workspace int    `json:"workspace"`
	// WorkspaceID is the ID of the window's workspace, numbered or hidden
	// (see Output.WorkspaceID).
	WorkspaceID string `json:"workspace_id"`
	Visible     bool   `json:"visible"`
	// Floating is set for native floats (dialogs) and stashed windows.
	Floating bool `json:"floating"`
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

func fromPorts(st ports.State, ids *workspaceid.IDs) State {
	out := State{Output: st.Output, Outputs: make([]Output, 0, len(st.Outputs)), Windows: make([]Window, 0, len(st.Windows))}
	for _, o := range st.Outputs {
		out.Outputs = append(out.Outputs, Output{Name: o.Name, Active: o.Active, Count: o.Count, Workspace: o.Workspace, WorkspaceID: ids.ID(o.WorkspaceID, o.Workspace)})
	}
	for _, w := range st.Windows {
		out.Windows = append(out.Windows, window(w, ids))
	}
	if st.Window != nil {
		w := window(*st.Window, ids)
		out.Window = &w
	}
	return out
}

func window(w ports.WindowState, ids *workspaceid.IDs) Window {
	return Window{ID: uint64(w.ID), AppID: w.AppID, PID: w.PID, Output: w.Output, Workspace: w.Workspace, WorkspaceID: ids.ID(w.WorkspaceID, w.WorkspaceName), Visible: w.Visible, Floating: w.Floating, StashIndex: w.StashIndex, StashCount: w.StashCount, Column: w.Column, Row: w.Row, Hidden: w.Hidden}
}

// Run writes every state it receives to path until ctx ends, then removes
// the file: a stale state must not outlive the session.
func Run(ctx context.Context, path string, states <-chan ports.State, ids *workspaceid.IDs, log zerowrap.Logger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// A file left by a crashed session on this socket is not ours.
	_ = os.Remove(path)
	defer os.Remove(path)
	for {
		select {
		case <-ctx.Done():
			return nil
		case st := <-states:
			if err := write(path, fromPorts(st, ids)); err != nil {
				// Scripts lose their state; the session goes on.
				log.Warn().Err(err).Str("path", path).Msg("write state")
			}
		}
	}
}

// write replaces the file atomically: readers never see half a state.
func write(path string, st State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

// Read loads the state file.
func Read(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

// procTree finds the parent of a process.
type procTree interface {
	Parent(pid int) (int, bool)
}

// linuxProcTree reads /proc/<pid>/stat.
type linuxProcTree struct{}

func (linuxProcTree) Parent(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// "pid (comm) state ppid ...": comm may hold spaces and parentheses.
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil && ppid > 0
}

// ErrNoWindow means no window belongs to the process or its ancestors.
var ErrNoWindow = errors.New("no window for this process")

// OutputOf finds the output showing a process: the window of the process
// itself or of its nearest ancestor, e.g. the terminal of a shell. Only
// application windows count: bars (layer surfaces) have no single output.
// A process with windows on several outputs resolves to its focused window,
// else a visible one. PIDs are those of the compositor's PID namespace.
func OutputOf(st State, pid int) (Output, error) {
	return outputOf(st, pid, linuxProcTree{})
}

func outputOf(st State, pid int, tree procTree) (Output, error) {
	byPID := map[int]Window{}
	for _, w := range st.Windows {
		if w.PID <= 0 {
			continue
		}
		cur, seen := byPID[w.PID]
		if !seen || better(st, w, cur) {
			byPID[w.PID] = w
		}
	}
	// A depth limit guards against a cycle from a reused PID mid-walk.
	for range 64 {
		if w, ok := byPID[pid]; ok {
			for _, o := range st.Outputs {
				if o.Name == w.Output {
					return o, nil
				}
			}
			return Output{}, fmt.Errorf("output %q not in state", w.Output)
		}
		parent, ok := tree.Parent(pid)
		if !ok || parent == pid {
			break
		}
		pid = parent
	}
	return Output{}, ErrNoWindow
}

// better prefers the focused window, then a visible one.
func better(st State, a, b Window) bool {
	focused := func(w Window) bool { return st.Window != nil && st.Window.ID == w.ID }
	if focused(a) != focused(b) {
		return focused(a)
	}
	return a.Visible && !b.Visible
}
