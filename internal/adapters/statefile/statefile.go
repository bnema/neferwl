// Package statefile publishes core's state as JSON for scripts, and reads it
// back for the `neferwl state` command. The JSON format is defined in statewire,
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
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/statewire"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
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

// writeInterval is the least time between two writes of the file.
const writeInterval = time.Second

// Run writes the states it receives to path until ctx ends, then removes
// the file: a stale state must not outlive the session. It writes at most
// once per writeInterval: a state after a quiet interval is written at once,
// others are coalesced and the latest is written when the interval ends.
func Run(ctx context.Context, path string, states <-chan domain.State, ids *workspaceid.IDs, log zerowrap.Logger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// A file left by a crashed session on this socket is not ours.
	_ = os.Remove(path)
	defer os.Remove(path)
	flush := func(st domain.State) {
		if err := write(path, statewire.FromDomain(st, ids)); err != nil {
			// Scripts lose their state; the session goes on.
			log.Warn().Err(err).Str("path", path).Msg("write state")
		}
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	var (
		last    time.Time
		pending domain.State
		waiting bool
	)
	for {
		var due <-chan time.Time
		if waiting {
			due = timer.C
		}
		select {
		case <-ctx.Done():
			return nil
		case st := <-states:
			if waiting {
				pending = st
				continue
			}
			if wait := writeInterval - time.Since(last); !last.IsZero() && wait > 0 {
				pending, waiting = st, true
				timer.Reset(wait)
				continue
			}
			flush(st)
			last = time.Now()
		case <-due:
			waiting = false
			flush(pending)
			pending = domain.State{}
			last = time.Now()
		}
	}
}

// write replaces the file atomically: readers never see half a state.
func write(path string, st statewire.State) error {
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
func Read(path string) (statewire.State, error) {
	var st statewire.State
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
func OutputOf(st statewire.State, pid int) (statewire.Output, error) {
	return outputOf(st, pid, linuxProcTree{})
}

func outputOf(st statewire.State, pid int, tree procTree) (statewire.Output, error) {
	byPID := map[int]statewire.Window{}
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
			return statewire.Output{}, fmt.Errorf("output %q not in state", w.Output)
		}
		parent, ok := tree.Parent(pid)
		if !ok || parent == pid {
			break
		}
		pid = parent
	}
	return statewire.Output{}, ErrNoWindow
}

// better prefers the focused window, then a visible one.
func better(st statewire.State, a, b statewire.Window) bool {
	focused := func(w statewire.Window) bool { return st.Window != nil && st.Window.ID == w.ID }
	if focused(a) != focused(b) {
		return focused(a)
	}
	return a.Visible && !b.Visible
}
