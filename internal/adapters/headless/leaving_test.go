package headless

import (
	"testing"

	"github.com/bnema/neferwl/internal/adapters/surfaces"
	"github.com/bnema/neferwl/internal/ports"
)

// A window drawn from a content its client withdrew (closed, fading out) is
// not reported shown: the empty content has no frame to present.
func TestFlipInfoSkipsKeptContent(t *testing.T) {
	table := surfaces.New()
	s := ports.Scene{OutputWidth: 2, OutputHeight: 2, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 2, H: 2}}, {ID: 2, Rect: ports.Rect{W: 1, H: 1}}}}
	table.Update(ports.SurfaceContent{ID: 1, Seq: 3, SHM: &ports.SHMBuffer{Pool: 1}}, s)
	table.Update(ports.SurfaceContent{ID: 2, Seq: 1, SHM: &ports.SHMBuffer{Pool: 2}}, s)
	if f := flipInfo(s, table.Map(), table.Kept); f.Shows[1] != 3 || f.Shows[2] != 1 {
		t.Fatalf("control: %v", f.Shows)
	}
	table.Update(ports.SurfaceContent{ID: 1, Seq: 4}, s)
	f := flipInfo(s, table.Map(), table.Kept)
	if _, ok := f.Shows[1]; ok || f.Shows[2] != 1 {
		t.Fatalf("a leaving window is reported shown: %v", f.Shows)
	}
}
