package wayland

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// The layout from core moves outputs after they were added at the startup
// placement: the log must show the real position.
func TestSetOutputsLogsPlacementChange(t *testing.T) {
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")
	h := newAdmissionHarnessWith(t, 1, Options{}, log)
	if added := out.String(); !strings.Contains(added, `"message":"output added"`) || !strings.Contains(added, `"x":0,"y":0`) {
		t.Fatalf("output added: %s", added)
	}
	placed := ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: 2, Height: 2}, X: 3200, Y: 40, Width: 2, Height: 2, Scale: 1, Primary: true}}
	h.s.display.Do(func() { h.s.setOutputs(ports.SetOutputs{Outputs: placed}) })
	if got := out.String(); !strings.Contains(got, `"message":"output changed"`) || !strings.Contains(got, `"x":3200,"y":40`) || !strings.Contains(got, `"primary":true`) || !strings.Contains(got, `"transform":0`) {
		t.Fatalf("placement change not logged: %s", got)
	}
	n := strings.Count(out.String(), `"output changed"`)
	h.s.display.Do(func() { h.s.setOutputs(ports.SetOutputs{Outputs: placed}) })
	if strings.Count(out.String(), `"output changed"`) != n {
		t.Fatalf("unchanged layout logged: %s", out.String())
	}
}
