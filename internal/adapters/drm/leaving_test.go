package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/adapters/surfaces"
	"github.com/bnema/neferwl/internal/ports"
)

// A window drawn from a content its client withdrew (it closed and fades
// out) never goes on a plane: wayland has released its buffer, so the
// client may already draw into it. The first leaving scene carries Fade 0
// and Zoom 0 (the fade starts at opaque), so the kept flag alone refuses it.
func TestLeavingWindowNeverOnAPlane(t *testing.T) {
	t.Run("overlay plane, a single leaving tile", func(t *testing.T) {
		o, _, _ := overlayOutput(t)
		o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
		table := surfaces.New()
		o.kept = table.Kept
		s, c := overlayScene()
		table.Update(c[2], s)
		// Window 2 unmaps: its empty content keeps the last one, Acquire
		// dropped, while the scene lists it.
		table.Update(ports.SurfaceContent{ID: 2}, s)
		if !table.Kept(2) {
			t.Fatal("setup: content not kept")
		}
		if _, _, _, reason := overlayCandidate(s, table.Map(), false, nil, o.kept); reason != "leaving" {
			t.Fatalf("overlay reason %q, want leaving", reason)
		}
		ov, rest := o.overlayFrame(s, table.Map())
		if ov.fb != 0 || len(rest.Windows) != 2 || o.overlayReason != "leaving" {
			t.Fatalf("leaving window put on the overlay: %+v rest %d reason %q", ov, len(rest.Windows), o.overlayReason)
		}
		// Without the kept flag the same scene goes on the plane: the
		// refusal is the flag's.
		if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "" {
			t.Fatalf("control: reason %q", reason)
		}
		// A new content of a remapped window frees it again.
		table.Update(c[2], s)
		if _, _, _, reason := overlayCandidate(s, table.Map(), false, nil, o.kept); reason != "" {
			t.Fatalf("remapped window refused: %q", reason)
		}
	})
	t.Run("direct scanout of a lone leaving fullscreen window", func(t *testing.T) {
		s, c := fullscreenScene()
		table := surfaces.New()
		for _, cc := range c {
			table.Update(cc, s)
		}
		id := s.Windows[0].ID
		if _, reason := scanoutCandidate(s, table.Map(), 200, 100, table.Kept); reason != "" {
			t.Fatalf("control: reason %q", reason)
		}
		table.Update(ports.SurfaceContent{ID: id}, s)
		if _, reason := scanoutCandidate(s, table.Map(), 200, 100, table.Kept); reason != "leaving" {
			t.Fatalf("scanout reason %q, want leaving", reason)
		}
		// Through the output: composed, reason logged.
		o, _, _ := testOutput(t)
		o.kept = table.Kept
		o.scanout = true
		if fb, _, _ := o.scanoutFrame(s, table.Map()); fb != 0 || o.reason != "leaving" {
			t.Fatalf("direct scanout of a leaving window: fb %d reason %q", fb, o.reason)
		}
	})
	t.Run("not reported shown", func(t *testing.T) {
		o, _, _ := testOutput(t)
		table := surfaces.New()
		o.kept = table.Kept
		s, c := overlayScene()
		table.Update(c[2], s)
		seen := map[ports.WindowID]uint64{2: 3}
		if shows := o.shownBy(s, seen); shows[2] != 3 {
			t.Fatalf("control: shows %v", shows)
		}
		table.Update(ports.SurfaceContent{ID: 2, Seq: 4}, s)
		seen[2] = 4
		if shows := o.shownBy(s, seen); len(shows) != 0 {
			t.Fatalf("a leaving window is reported shown: %v", shows)
		}
	})
}
