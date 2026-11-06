package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSessionEnvironmentAndArgs(t *testing.T) {
	environ := []string{"PATH=/bin", "NEFERWL_STATE=x", "NEFERWL_SLOT=x", "NEFERWL_CONTROL=x", "NEFERWL_SESSION=x", "WAYLAND_DISPLAY=x", "WAYLAND_SOCKET=x", "DISPLAY=x", "DBUS_SESSION_BUS_ADDRESS=bus", "HOME=/home/test"}
	if got := sessionEnvironment(environ, false); !reflect.DeepEqual(got, []string{"PATH=/bin", "HOME=/home/test"}) {
		t.Fatal(got)
	}
	if got := sessionEnvironment(environ, true); !reflect.DeepEqual(got, []string{"PATH=/bin", "DBUS_SESSION_BUS_ADDRESS=bus", "HOME=/home/test"}) {
		t.Fatal(got)
	}
	want := []string{"--backend=headless", "--profile=persistent", "--session-id=test", "--ready-fd=3", "--size=100x80", "--config=/tmp/config", "--timeout=1m0s", "--no-xwayland"}
	if got := sessionArgs("test", "persistent", "100x80", "/tmp/config", time.Minute, []string{"--no-xwayland"}); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestSessionHelperProcess(t *testing.T) {
	mode := os.Getenv("NEFERWL_TEST_SESSION_CHILD")
	if mode == "" {
		return
	}
	if mode == "cancel" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		if err := os.WriteFile(os.Getenv("NEFERWL_TEST_CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(8)
		}
		if err := syscall.Kill(os.Getppid(), syscall.SIGINT); err != nil {
			os.Exit(9)
		}
		<-ctx.Done()
		os.Exit(10)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	f := os.NewFile(3, "ready")
	if mode == "bad" {
		_, _ = f.WriteString("not json\n")
		_ = f.Close()
		os.Exit(0)
	}
	_ = json.NewEncoder(f).Encode(sessionReady{ID: "test", Wayland: "wayland-test", Control: "/tmp/test.sock"})
	_ = f.Close()
	os.Exit(0)
}

//testpolicy:realtime Detached helper processes and readiness pipes are real OS I/O.
func TestStartSessionReady(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ready", "fail", "bad"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r, err := startSession(ctx, exe, []string{"-test.run=^TestSessionHelperProcess$"}, append(sessionEnvironment(os.Environ(), false), "NEFERWL_TEST_SESSION_CHILD="+mode))
		cancel()
		if mode == "ready" {
			if err != nil || r.ID != "test" {
				t.Fatal(r, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "exit status") {
			t.Fatal(err)
		}
	}
}

// TestStartSessionSignalCancellation: a detached helper sends SIGINT during
// startup; process exit and reaping use real OS I/O.
func TestStartSessionSignalCancellation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	// Without the production signal handling, the helper's SIGINT kills this
	// test binary instead of cancelling startup.
	_, err = startSessionInterruptible(t.Context(), 10*time.Second, exe, []string{"-test.run=^TestSessionHelperProcess$"}, append(sessionEnvironment(os.Environ(), false), "NEFERWL_TEST_SESSION_CHILD=cancel", "NEFERWL_TEST_CHILD_PID="+pidPath))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startup: %v", err)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d still exists after cancellation: %v", pid, err)
	}
}
