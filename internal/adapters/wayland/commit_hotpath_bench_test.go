package wayland

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/fifo"
	"github.com/bnema/go-wayland-bindings/server/viewporter"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// BenchmarkProtocolQueuedCommit measures a real client's requests and server
// dispatch, queue capture, and paced application. Socket roundtrips synchronize
// each frame; this is end-to-end latency, not an allocation-free apply microbench.
func BenchmarkProtocolQueuedCommit(b *testing.B) {
	dir := b.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	c, err := wlturbo.Connect(filepath.Join(dir, s.SocketName()))
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		_ = c.Close()
		forgetWireClient(c)
	}()
	if err := c.Roundtrip(); err != nil {
		b.Fatal(err)
	}
	bind := func(name string) uint32 {
		g, ok := c.Registry().FindGlobal(name)
		if !ok {
			b.Fatalf("missing %s", name)
		}
		id, err := bindWireID(c, g.Name, g.Interface, 1)
		if err != nil {
			b.Fatal(err)
		}
		return id
	}
	send := func(id, op uint32, args ...any) {
		if err := wireRequest(c, id, uint16(op), nil, args...); err != nil {
			b.Fatal(err)
		}
	}
	comp, fm, vpm := bind("wl_compositor"), bind("wp_fifo_manager_v1"), bind("wp_viewporter")
	shm := bind("wl_shm")
	register := func(id uint32) {
		p := &protocolProxy{}
		p.SetID(id)
		registerWireProxy(c, p)
	}
	register(shm)
	surf := c.AllocateID()
	send(comp, wayland.CompositorRequestCreateSurface, surf)
	register(surf)
	f := c.AllocateID()
	send(fm, fifo.WpFifoManagerV1RequestGetFifo, f, surf)
	register(f)
	vp := c.AllocateID()
	send(vpm, viewporter.WpViewporterRequestGetViewport, vp, surf)
	register(vp)
	// One real SHM buffer, reused for every commit.
	fd, err := unix.MemfdCreate("bench-buffer", 0)
	if err != nil {
		b.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		b.Fatal(err)
	}
	pool, buf := c.AllocateID(), c.AllocateID()
	register(pool)
	register(buf)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		b.Fatal(err)
	}
	send(pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1), int32(1), int32(4), uint32(0))
	if err := c.Roundtrip(); err != nil {
		b.Fatal(err)
	}
	// Install a barrier so each measured commit really waits in the queue.
	send(f, fifo.WpFifoV1RequestSetBarrier)
	send(surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		send(f, fifo.WpFifoV1RequestWaitBarrier)
		send(surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		send(surf, wayland.SurfaceRequestDamage, int32(0), int32(0), int32(1), int32(1))
		send(vp, viewporter.WpViewportRequestSetDestination, int32(1), int32(1))
		send(surf, wayland.SurfaceRequestCommit)
		// Later requests must not be consumed by the captured update.
		send(vp, viewporter.WpViewportRequestSetDestination, int32(2), int32(2))
		if err := c.Roundtrip(); err != nil {
			b.Fatal(err)
		}
		s.display.Do(func() {
			for _, state := range s.surfaces {
				if state.wl != nil && state.wl.ID() == surf {
					if len(state.queue) != 1 {
						b.Errorf("queued commits: %d", len(state.queue))
						return
					}
					state.barrier = false
					s.tickFifo(time.Now(), nil)
					if len(state.queue) != 0 {
						b.Error("commit still queued")
					}
					return
				}
			}
			b.Error("missing surface")
		})
		// Next iteration overwrites the pending viewport destination.
		send(f, fifo.WpFifoV1RequestSetBarrier)
		send(surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			b.Fatal(err)
		}
	}
}
