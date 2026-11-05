package statefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
)

var sample = State{
	Output:  "DP-2",
	Outputs: []Output{{Name: "HDMI-A-1", Active: 1, Count: 1, WorkspaceID: "p-1"}, {Name: "DP-2", Active: 2, Count: 3, Workspace: "web", WorkspaceID: "name:web"}},
	Windows: []Window{{ID: 1, AppID: "foot", PID: 100, Output: "HDMI-A-1", Workspace: 1, WorkspaceID: "p-1", Visible: true}, {ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 2, WorkspaceID: "name:web", Visible: true}},
}

// The file holds the latest state while the session runs; a stale file from
// a crashed session goes at start, and the file is removed at exit.
func TestRunWritesAndRemoves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		states := make(chan domain.State, 1)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, path, states, workspaceid.WithPrefix("0a1b2c3d"), zerowrap.Default()) }()
		focused := domain.WindowState{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: 9, Visible: true}
		states <- domain.State{Output: "DP-2", Outputs: []domain.OutputState{{Name: "DP-2", Active: 1, Count: 1, WorkspaceID: 9}}, Window: &focused, Windows: []domain.WindowState{focused}}
		want := State{Output: "DP-2", Outputs: []Output{{Name: "DP-2", Active: 1, Count: 1, WorkspaceID: "0a1b2c3d-9"}}, Window: &Window{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: "0a1b2c3d-9", Visible: true}, Windows: []Window{{ID: 2, AppID: "foot", PID: 200, Output: "DP-2", Workspace: 1, WorkspaceID: "0a1b2c3d-9", Visible: true}}, Workspaces: []Workspace{}}
		synctest.Wait()
		got, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v", got)
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

// The file is written at most once per second: the first state and one after
// a quiet second at once, a burst within the second as one trailing write of
// the latest state.
func TestRunRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s", "wayland-9.json")
		states := make(chan domain.State)
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
		send := func(name string) { states <- domain.State{Output: name} }

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
