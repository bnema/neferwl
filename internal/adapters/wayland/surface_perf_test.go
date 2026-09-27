package wayland

import (
	"context"
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// TestTiledCommitPublishAllocations exercises real content publication without
// a client connection, holding old snapshots while subsequent commits publish.
func TestTiledCommitPublishAllocations(t *testing.T) {
	for _, count := range []int{16, 64} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			channels := Channels{Contents: make(chan ports.SurfaceContent, 1)}
			srv, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, channels, logging.For(context.Background(), "wayland"))
			if err != nil {
				t.Fatal(err)
			}
			root := &surface{server: srv, content: ports.SurfaceContent{Width: 1, Height: 1}, has: true, identity: 1}
			root.xdg = &xdgSurface{window: &window{id: 1, mapped: true}}
			for i := range count {
				child := &surface{server: srv, has: true, content: ports.SurfaceContent{Width: 1, Height: 1}, identity: uint64(i + 2)}
				child.sub.parent = root
				root.sub.children = append(root.sub.children, child)
				root.sub.layout = append(root.sub.layout, childLayout{child: child, x: i})
			}
			publish := func() { root.redraw() }
			root.treeDirty = true
			publish()
			first := srv.contents[1]
			publish()
			if first.Seq != 1 || len(first.Children) != count || len(first.DamageHistory) != 1 {
				t.Fatalf("previous publication changed: %+v", first)
			}
			if allocs := testing.AllocsPerRun(100, publish); allocs > 4 {
				t.Errorf("%d children: %.1f allocs/publication; want <=4", count, allocs)
			} else {
				t.Logf("%d children: %.1f allocs/unchanged publication", count, allocs)
			}
			// A queued commit with an unchanged layout must not allocate
			// proportionally to the number of children.
			root.sub.pendingLayout = root.sub.layout
			queue := func() { _ = root.takePending() }
			if allocs := testing.AllocsPerRun(100, queue); allocs > 2 {
				t.Errorf("%d children: %.1f allocs/queued commit; want <=2", count, allocs)
			}
			if first.Seq != 1 || len(first.Children) != count || len(first.DamageHistory) != 1 || first.DamageHistory[0].Seq != 1 {
				t.Fatal("previous publication was mutated")
			}
			// An applied child change rebuilds into a distinct immutable slice.
			root.sub.children[count-1].version++
			root.treeDirty = true
			publish()
			if first.Children[count-1].Version != 0 || srv.contents[1].Children[count-1].Version != 1 {
				t.Fatal("child version leaked into an earlier publication")
			}
			if &first.Children[0] == &srv.contents[1].Children[0] {
				t.Fatal("rebuild reused previously published children storage")
			}
		})
	}
}
