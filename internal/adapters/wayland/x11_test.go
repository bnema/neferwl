package wayland

import (
	"context"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

// Mapping asks the production PID classifier; nil never marks a native client.
func TestWindowXwayland(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "x11"}[enabled], func(t *testing.T) {
			opts := Options{}
			if enabled {
				x11 := portsmocks.NewMockX11Clients(t)
				x11.EXPECT().Owns(os.Getpid()).Return(true)
				opts.X11Clients = x11
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
