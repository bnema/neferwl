package wayland

import (
	"context"
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// TestEffectiveInputEmptyTreeAllocations guards against a heap-allocated
// recursive closure for every input-region traversal of a tiled tree.
func TestEffectiveInputEmptyTreeAllocations(t *testing.T) {
	root := &surface{}
	for range 64 {
		child := &surface{}
		root.sub.children = append(root.sub.children, child)
		root.sub.layout = append(root.sub.layout, childLayout{child: child})
	}
	walk := func() {
		all, rects := root.effectiveInput()
		if all || len(rects) != 0 {
			t.Fatal("empty tree has input")
		}
	}
	if allocs := testing.AllocsPerRun(100, walk); allocs != 0 {
		t.Errorf("empty tiled input traversal: %.1f allocs, want 0", allocs)
	}
}

// TestTiledCommitPublishAllocations exercises real content publication without
// a client connection, holding old snapshots while subsequent commits publish.
func TestTiledCommitPublishAllocations(t *testing.T) {
	// The top-level test is listed in Makefile perf-check; this subtest guards
	// the per-commit update storage after its first retirement.
	t.Run("queueUpdateSteadyState", func(t *testing.T) {
		srv := &Server{fifoSurfaces: make(map[*surface]struct{}), frameReady: make(chan struct{}, 1)}
		surf := &surface{server: srv}
		commit := func() {
			surf.queueUpdate()
			surf.dropQueue()
		}
		commit()
		if allocs := testing.AllocsPerRun(100, commit); allocs != 0 {
			t.Errorf("steady-state queueUpdate/dropQueue: %.1f allocs/commit; want 0", allocs)
		}
	})
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
			if allocs := testing.AllocsPerRun(100, publish); allocs > 2 {
				t.Errorf("%d children: %.1f allocs/publication; want <=2", count, allocs)
			} else {
				t.Logf("%d children: %.1f allocs/unchanged publication", count, allocs)
			}
			// A queued commit with an unchanged layout must not allocate
			// proportionally to the number of children.
			root.sub.pendingLayout = root.sub.layout
			queue := func() { _ = root.takePending() }
			if allocs := testing.AllocsPerRun(100, queue); allocs > 0 {
				t.Errorf("%d children: %.1f allocs/queued commit; want 0", count, allocs)
			} else {
				t.Logf("%d children: %.1f allocs/takePending", count, allocs)
			}
			commit := func() {
				root.queueUpdate()
				root.dropQueue()
			}
			if allocs := testing.AllocsPerRun(100, commit); allocs != 0 {
				t.Errorf("%d children: %.1f allocs/queueUpdate; want 0", count, allocs)
			} else {
				t.Logf("%d children: %.1f allocs/queueUpdate", count, allocs)
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
			// Rebuilding a changed child's immutable snapshot must have a
			// constant allocation count, regardless of tree size.
			child := root.sub.children[count-1]
			changed := func() {
				child.version++
				root.treeDirty = true
				root.redraw()
			}
			if allocs := testing.AllocsPerRun(100, changed); allocs > 3 {
				t.Errorf("%d children: %.1f allocs/changed publication; want <=3", count, allocs)
			} else {
				t.Logf("%d children: %.1f allocs/changed publication", count, allocs)
			}
		})
	}
}
