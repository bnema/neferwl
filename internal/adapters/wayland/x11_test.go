package wayland

import (
	"context"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// Mapping asks the production PID classifier; nil never marks a native client.
func TestWindowXwayland(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "x11"}[enabled], func(t *testing.T) {
			opts := Options{}
			if enabled {
				opts.X11Client = func(pid int) bool { return pid == os.Getpid() }
			}
			s, events, _, _, dir := contentServerWith(t, opts, logging.For(context.Background(), "wayland"))
			c := protocolClient(t, s, dir)
			w := toplevelMapper(t, c, events)()
			if w.PID != os.Getpid() || w.Xwayland != enabled {
				t.Fatalf("mapped PID=%d Xwayland=%v, want PID=%d Xwayland=%v", w.PID, w.Xwayland, os.Getpid(), enabled)
			}
		})
	}
}
