package statefile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

var sample = State{
	Output:  "DP-2",
	Outputs: []Output{{Name: "HDMI-A-1", Active: 1, Count: 1, WorkspaceID: "p-1"}, {Name: "DP-2", Active: 2, Count: 3, Workspace: "web", WorkspaceID: "name:web"}},
	Windows: []Window{{ID: 1, AppID: "foot", PID: 100, Output: "HDMI-A-1", Workspace: 1, WorkspaceID: "p-1", Visible: true}, {ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 2, WorkspaceID: "name:web", Visible: true}},
}

// The file holds the latest state while the session runs; a stale file from
// a crashed session goes at start, and the file is removed at exit.
func TestRunWritesAndRemoves(t *testing.T) {
	path, err := Path(t.TempDir(), "wayland-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"output":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	states := make(chan ports.State, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, path, states, workspaceid.WithPrefix("0a1b2c3d"), zerowrap.Default()) }()
	focused := ports.WindowState{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: 9, Visible: true}
	states <- ports.State{Output: "DP-2", Outputs: []ports.OutputState{{Name: "DP-2", Active: 1, Count: 1, WorkspaceID: 9}}, Window: &focused, Windows: []ports.WindowState{focused}}
	want := State{Output: "DP-2", Outputs: []Output{{Name: "DP-2", Active: 1, Count: 1, WorkspaceID: "0a1b2c3d-9"}}, Window: &Window{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: "0a1b2c3d-9", Visible: true}, Windows: []Window{{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: "0a1b2c3d-9", Visible: true}}, Workspaces: []Workspace{}}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got, err := Read(path); err == nil && got.Output != "stale" {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no state file")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("state file left behind", err)
	}
}

// Stash and cell fields reach the file with their script-facing names.
func TestWindowScriptFields(t *testing.T) {
	got := window(ports.WindowState{ID: 3, Floating: true, Fullscreen: true, StashIndex: 2, StashCount: 4, Column: 5, Row: 6, Hidden: true}, workspaceid.WithPrefix("0a1b2c3d"))
	if !got.Floating || !got.Fullscreen || got.StashIndex != 2 || got.StashCount != 4 || got.Column != 5 || got.Row != 6 || !got.Hidden {
		t.Fatalf("%+v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"floating":true`, `"fullscreen":true`, `"stash_index":2`, `"stash_count":4`, `"column":5`, `"row":6`, `"hidden":true`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("%s missing %s", data, key)
		}
	}
}

func TestPath(t *testing.T) {
	if p, err := Path("/run/user/1", "/run/user/1/wayland-1"); err != nil || p != "/run/user/1/neferwl/wayland-1.json" {
		t.Fatal(p, err)
	}
	for _, c := range [][2]string{{"", "wayland-1"}, {"/run/user/1", ""}} {
		if _, err := Path(c[0], c[1]); err == nil {
			t.Fatal(c)
		}
	}
}

// A shell finds its window through its ancestors.
func TestOutputOfWalksParents(t *testing.T) {
	tree := newMockprocTree(t)
	// shell 300 → login 250 → terminal 200 (window on DP-2)
	tree.EXPECT().Parent(300).Return(250, true)
	tree.EXPECT().Parent(250).Return(200, true)
	o, err := outputOf(sample, 300, tree)
	if err != nil || o.Name != "DP-2" || o.Active != 2 || o.Count != 3 {
		t.Fatal(o, err)
	}
	// The window's own process needs no walk.
	if o, err := outputOf(sample, 100, newMockprocTree(t)); err != nil || o.Name != "HDMI-A-1" {
		t.Fatal(o, err)
	}
	// No window up to init.
	orphan := newMockprocTree(t)
	orphan.EXPECT().Parent(400).Return(1, true)
	orphan.EXPECT().Parent(1).Return(0, false)
	if _, err := outputOf(sample, 400, orphan); !errors.Is(err, ErrNoWindow) {
		t.Fatal(err)
	}
}

// One process with windows on two outputs (a terminal server): the focused
// window wins, else a visible one.
func TestOutputOfPrefersFocusedWindow(t *testing.T) {
	st := sample
	st.Windows = []Window{
		{ID: 1, PID: 100, Output: "HDMI-A-1", Visible: true},
		{ID: 2, PID: 100, Output: "DP-2", Visible: false},
		{ID: 3, PID: 100, Output: "DP-2", Visible: true},
	}
	if o, _ := outputOf(st, 100, newMockprocTree(t)); o.Name != "HDMI-A-1" {
		t.Fatal(o)
	}
	st.Window = &st.Windows[2]
	if o, _ := outputOf(st, 100, newMockprocTree(t)); o.Name != "DP-2" {
		t.Fatal(o)
	}
}

func TestLinuxProcTreeParent(t *testing.T) {
	if ppid, ok := (linuxProcTree{}).Parent(os.Getpid()); !ok || ppid != os.Getppid() {
		t.Fatal(ppid, ok)
	}
}

// A configured workspace is named after its configured name, any other one
// after the launch prefix and its core ID: the string of ext_workspace_handle_v1.
func TestWorkspaceIDStrings(t *testing.T) {
	ids := workspaceid.WithPrefix("0a1b2c3d")
	st := fromPorts(ports.State{
		Outputs: []ports.OutputState{{Name: "DP-2", Workspace: "web", WorkspaceID: 7}, {Name: "DP-3", WorkspaceID: 8}},
		Windows: []ports.WindowState{{ID: 1, WorkspaceID: 7, WorkspaceName: "web"}, {ID: 2, WorkspaceID: 8}},
	}, ids)
	if st.Outputs[0].WorkspaceID != "name:web" || st.Outputs[1].WorkspaceID != "0a1b2c3d-8" {
		t.Fatalf("outputs %+v", st.Outputs)
	}
	if st.Windows[0].WorkspaceID != "name:web" || st.Windows[1].WorkspaceID != "0a1b2c3d-8" {
		t.Fatalf("windows %+v", st.Windows)
	}
	data, err := json.Marshal(st.Windows[1])
	if err != nil || !strings.Contains(string(data), `"workspace_id":"0a1b2c3d-8"`) {
		t.Fatalf("%s %v", data, err)
	}
}

// Workspaces reach the file with their script-facing names.
func TestWorkspaceGeometryFields(t *testing.T) {
	ids := workspaceid.WithPrefix("0a1b2c3d")
	st := fromPorts(ports.State{Workspaces: []ports.WorkspaceState{
		{ID: 7, Number: 2, Name: "web", Output: "DP-2", Visible: true, Overflow: "fixed", Columns: []ports.ColumnState{
			{Rect: ports.Rect{X: 10, Y: 20, W: 300, H: 400}, Windows: []ports.TileState{{ID: 5, Rect: ports.Rect{X: 11, Y: 21, W: 298, H: 198}}}},
		}},
		{ID: 8, Output: "DP-2"},
	}}, ids)
	want := []Workspace{
		{ID: "name:web", Number: 2, Name: "web", Output: "DP-2", Visible: true, Overflow: "fixed", Columns: []Column{
			{X: 10, Y: 20, Width: 300, Height: 400, Windows: []Tile{{ID: 5, X: 11, Y: 21, Width: 298, Height: 198}}},
		}},
		{ID: "0a1b2c3d-8", Output: "DP-2", Columns: []Column{}},
	}
	if !reflect.DeepEqual(st.Workspaces, want) {
		t.Fatalf("%+v", st.Workspaces)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"workspaces":[`, `"id":"name:web"`, `"number":2`, `"visible":true`, `"overflow":"fixed"`, `"columns":[{"x":10,"y":20,"width":300,"height":400,"windows":[{"id":5,"x":11,"y":21,"width":298,"height":198}]}]`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("%s missing %s", data, key)
		}
	}
}

// The file is written at most once per second: the first state and one after
// a quiet second at once, a burst within the second as one trailing write of
// the latest state.
func TestRunRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s", "wayland-9.json")
		states := make(chan ports.State)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, path, states, workspaceid.WithPrefix("0a1b2c3d"), zerowrap.Default()) }()
		output := func() string {
			t.Helper()
			synctest.Wait()
			st, err := Read(path)
			if err != nil {
				return ""
			}
			return st.Output
		}
		send := func(name string) { states <- ports.State{Output: name} }

		send("a")
		if got := output(); got != "a" {
			t.Fatalf("first state not written at once: %q", got)
		}
		// A burst within the second: nothing yet, then the latest only.
		time.Sleep(200 * time.Millisecond)
		send("b")
		send("c")
		send("d")
		if got := output(); got != "a" {
			t.Fatalf("written within the second: %q", got)
		}
		time.Sleep(799 * time.Millisecond)
		if got := output(); got != "a" {
			t.Fatalf("written before the second ended: %q", got)
		}
		time.Sleep(time.Millisecond)
		if got := output(); got != "d" {
			t.Fatalf("trailing write: %q, want d", got)
		}
		// Nothing more is pending: the file stays as it is.
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		if got := output(); got != "" {
			t.Fatalf("a write with no new state: %q", got)
		}
		// After a quiet period the next state is immediate.
		send("e")
		if got := output(); got != "e" {
			t.Fatalf("state after a quiet period: %q", got)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("state file left behind", err)
		}
	})
}
