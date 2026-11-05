package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates the package from the developer's live session: control
// and state targets are cleared and the runtime directory is private. Tests
// set only what they need with t.Setenv.
func TestMain(m *testing.M) {
	runtime, err := os.MkdirTemp("", "neferwl-cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, key := range []string{"NEFERWL_SESSION", "NEFERWL_CONTROL", "NEFERWL_STATE", "WAYLAND_DISPLAY"} {
		_ = os.Unsetenv(key)
	}
	_ = os.Setenv("XDG_RUNTIME_DIR", runtime)
	code := m.Run()
	_ = os.RemoveAll(runtime)
	os.Exit(code)
}
