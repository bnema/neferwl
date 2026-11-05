package cli

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
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
	if mode == "fail" {
		os.Exit(7)
	}
	f := os.NewFile(3, "ready")
	if mode == "bad" {
		_, _ = f.WriteString("not json\n")
		f.Close()
		os.Exit(0)
	}
	_ = json.NewEncoder(f).Encode(sessionReady{ID: "test", Wayland: "wayland-test", Control: "/tmp/test.sock"})
	f.Close()
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
