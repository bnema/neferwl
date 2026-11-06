package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

//testpolicy:realtime bounded wait for a real subprocess to write its runtime crash report
func TestCrashOutputWorkerTraceback(t *testing.T) {
	const childEnv = "NEFERWL_TEST_CRASH_WORKER"
	if os.Getenv(childEnv) == "1" {
		_, _, err := Open(context.Background(), "headless", "info", "")
		if err != nil {
			t.Fatal(err)
		}
		go func() { panic("logging worker crash regression") }()
		select {} // keep the test's goroutine alive until the worker panics
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("GOTRACEBACK", "none")
	t.Setenv(childEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashOutputWorkerTraceback$")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("crash subprocess timed out: %v\n%s", ctx.Err(), out)
	}
	if err == nil {
		t.Fatalf("crash subprocess succeeded: %s", out)
	}
	paths, err := filepath.Glob(filepath.Join(state, "neferwl", "runs", "headless", "*.crash"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("crash files %v: %v; child output: %s", paths, err, out)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"logging worker crash regression", "goroutine", "TestCrashOutputWorkerTraceback"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("crash report missing %q:\n%s", want, data)
		}
	}
}

func TestParseDebug(t *testing.T) {
	for _, tc := range []struct {
		input     string
		wantError bool
		selected  []string
	}{
		{"", false, nil}, {"core,app", false, []string{"core", "app"}},
		{"all", false, []string{"all"}}, {"unknown", true, nil}, {"core,", true, nil},
		{"all,input-motion", false, []string{"all", "input-motion"}},
	} {
		got, err := ParseDebug(tc.input)
		if (err != nil) != tc.wantError {
			t.Fatalf("ParseDebug(%q): %v", tc.input, err)
		}
		for _, name := range tc.selected {
			if !got[name] {
				t.Errorf("ParseDebug(%q) missing %s", tc.input, name)
			}
		}
	}
}

func TestComponentLevelsAndRotation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "neferwl", "runs", "drm")
		path := filepath.Join(dir, "latest.log")
		for run := 0; run < keepRuns+2; run++ {
			time.Sleep(2 * time.Millisecond) // distinct millisecond timestamps
			ctx, closeLog, err := Open(context.Background(), "drm", "info", "core")
			if err != nil {
				t.Fatal(err)
			}
			core := For(ctx, "core")
			app := For(ctx, "app")
			core.Debug().Msg("visible")
			app.Debug().Msg("hidden")
			app.Info().Msg("startup")
			if err := closeLog(false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := splitLines(data)
			if len(lines) != 2 {
				t.Fatalf("expected 2 entries, got %q", data)
			}
			for _, line := range lines {
				var entry map[string]any
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatal(err)
				}
				if entry["component"] != "core" && entry["component"] != "app" {
					t.Fatalf("missing component: %v", entry)
				}
			}
		}
		runs, _ := filepath.Glob(filepath.Join(dir, "2*.log"))
		if len(runs) != keepRuns {
			t.Fatalf("kept %d runs, want %d", len(runs), keepRuns)
		}
		// A clean run leaves no empty crash file behind.
		if crashes, _ := filepath.Glob(filepath.Join(dir, "2*.crash")); len(crashes) != 0 {
			t.Fatalf("empty crash files kept: %v", crashes)
		}
	})
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// Headless runs rotate in their own directory: they never prune a DRM run.
func TestBackendsRotateSeparately(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	synctest.Test(t, func(t *testing.T) {
		runs := filepath.Join(os.Getenv("XDG_STATE_HOME"), "neferwl", "runs")
		open := func(backend string) {
			time.Sleep(2 * time.Millisecond) // distinct millisecond timestamps
			_, closeLog, err := Open(context.Background(), backend, "info", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := closeLog(false); err != nil {
				t.Fatal(err)
			}
		}
		open("drm")
		for range keepRuns + 2 {
			open("headless")
		}
		if drm, _ := filepath.Glob(filepath.Join(runs, "drm", "2*.log")); len(drm) != 1 {
			t.Fatalf("drm runs %v", drm)
		}
		if _, err := os.Stat(filepath.Join(runs, "drm", "latest.log")); err != nil {
			t.Fatal(err)
		}
		if headless, _ := filepath.Glob(filepath.Join(runs, "headless", "2*.log")); len(headless) != keepRuns {
			t.Fatalf("kept %d headless runs", len(headless))
		}
	})
}

// A category alone raises its component to debug, so its lines are written.
func TestCategoryEnablesComponentDebug(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, closeLog, err := Open(context.Background(), "drm", "info", "input-motion")
	if err != nil {
		t.Fatal(err)
	}
	input, core := For(ctx, "input"), For(ctx, "core")
	input.Debug().Msg("motion")
	core.Debug().Msg("hidden")
	if err := closeLog(false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "neferwl", "runs", "drm", "latest.log"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := splitLines(data); len(lines) != 1 || !bytes.Contains(lines[0], []byte(`"motion"`)) {
		t.Fatalf("got %q", data)
	}
}

// A disk that stops answering must not block the goroutine that logs:
// input and outputs log from their own loops.
//
//testpolicy:realtime a real OS pipe simulates a blocked disk writer
func TestLogWriteNeverBlocksOnSlowDisk(t *testing.T) {
	r, pw, err := os.Pipe() // nobody reads: writes block once the pipe is full
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	w := newAsyncWriter(struct{ io.Writer }{pw}, pw, "")
	line := bytes.Repeat([]byte{'x'}, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 4 * logQueue { // far past the pipe buffer and the queue
			_, _ = w.Write(line)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("log write blocked on a stuck disk")
	}
	_ = r.Close() // unblock the writer so Close can drain and return
	_ = w.Close()
}

// Narrow categories are on only when named, never through "all".
func TestEnabledCategories(t *testing.T) {
	// The component each narrow category logs under; "" is not asserted.
	owners := map[string]string{"input-motion": "", "input-keys": "", "drm-flip": "", "wayland-damage": "wayland", "render-damage": "render"}
	for category, owner := range owners {
		component := categories[category]
		if owner != "" && component != owner {
			t.Errorf("%s belongs to %q, want %q", category, component, owner)
		}
		for _, tc := range []struct {
			debug string
			want  bool
		}{{"all", false}, {component, false}, {category, true}, {"all," + category, true}} {
			sel, err := ParseDebug(tc.debug)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), debugKey{}, debugSet(sel))
			if got := Enabled(ctx, category); got != tc.want {
				t.Errorf("%s with %q: %v", category, tc.debug, got)
			}
		}
	}
}

// TestOpenSessionPrunesOldSessions: a session's runs go to sessions/<id>/;
// the newest keepRuns session directories are kept by modification time,
// and an old one whose lock is held by a live session survives.
func TestOpenSessionPrunesOldSessions(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := filepath.Join(state, "neferwl", "sessions")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range keepRuns + 5 {
		dir := filepath.Join(root, fmt.Sprintf("s-%02d", i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	// s-00 is the oldest but live: another holder has its lock.
	live, err := os.OpenFile(filepath.Join(root, "s-00", "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()
	if err := syscall.Flock(int(live.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	mod := base
	if err := os.Chtimes(filepath.Join(root, "s-00"), mod, mod); err != nil {
		t.Fatal(err)
	}

	_, closeLog, err := OpenSession(context.Background(), "current", "info", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "current", "latest.log")); err != nil {
		t.Fatal(err)
	}
	// A second open of the same live session is refused.
	if _, _, err := OpenSession(context.Background(), "current", "info", ""); err == nil {
		t.Fatal("opened a session log twice")
	}
	if err := closeLog(false); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// current + the newest keepRuns-1 others + live s-00.
	want := []string{"current", "s-00"}
	for i := 6; i < keepRuns+5; i++ {
		want = append(want, fmt.Sprintf("s-%02d", i))
	}
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("kept %v, want %v", names, want)
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if _, _, err := OpenSession(context.Background(), bad, "info", ""); err == nil {
			t.Errorf("accepted session id %q", bad)
		}
	}
}

// Root lifecycle ownership excludes other openers and pruners on the same
// inode, including after release and reacquisition. No scheduling or waits
// are needed to probe real flock exclusion.
func TestSessionLifecycleLock(t *testing.T) {
	root := t.TempDir()
	for range 2 {
		owner, err := lockSessionRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		probe, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_RDWR, 0)
		if err != nil {
			_ = owner.Close()
			t.Fatal(err)
		}
		err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EWOULDBLOCK {
			t.Errorf("lifecycle lock not exclusive: %v", err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Errorf("lifecycle lock not released: %v", err)
		}
		_ = probe.Close()
	}
}
