package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestContentSubsurfaceWindowFacts(t *testing.T) {
	for _, removal := range []string{"revert", "unmap", "destroy", "remap"} {
		t.Run(removal, func(t *testing.T) {
			events := make(chan ports.ClientEvent, 32)
			s, dir, _ := idleClockServerWith(t, Channels{Events: events})
			c := protocolClient(t, s, dir)
			w, root, xdg := surfaceMapper(t, c, events)()
			comp := bindProtocol(t, c, "wl_compositor")
			subc := bindProtocol(t, c, "wl_subcompositor")
			child, sub := c.AllocateID(), c.AllocateID()
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
			registerProtocol(t, c, child)
			requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
			registerProtocol(t, c, sub)
			requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetDesync)
			requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
			require.NoError(t, c.Roundtrip())
			var content *surface
			s.display.Do(func() {
				content = s.windows[w.ID].surf().sub.children[0]
				// Exercise the same captured colour state as the colour protocol sets.
				content.next.color = SurfaceColor{Set: true, TF: pq, Primaries: bt2020}
				s.watchStats(w.ID)
			})
			now := time.Unix(1, 0)
			s.clock.(*portsmocks.MockClock).EXPECT().Now().RunAndReturn(func() time.Time { result := now; now = now.Add(20 * time.Millisecond); return result }).Times(3)
			b := shmBuffer(t, c)
			for range 3 {
				requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
				requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
				// Equal-sized fresh root buffers must not add a second timing stream.
				if removal == "revert" {
					requestProtocol(t, c, root, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
					requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
				}
				require.NoError(t, c.Roundtrip())
			}
			require.Equal(t, ports.WindowColorDescription{ID: w.ID, PlainSDR: false}, clientEvent[ports.WindowColorDescription](t, events))
			s.display.Do(func() {
				require.Equal(t, float32(50), s.stats.snapshot().ClientFPS)
				require.Same(t, content, s.stats.source)
				require.Equal(t, 1, s.stats.facts.Width)
			})
			switch removal {
			case "remap":
				s.display.Do(func() { s.watchStats(0) })
				requestProtocol(t, c, root, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
				requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
				require.NoError(t, c.Roundtrip())
				_ = clientEvent[ports.WindowUnmapped](t, events)
				serials := make(chan uint32, 8)
				xp := &configureProxy{serial: serials}
				xp.SetID(xdg)
				registerWireProxy(c, xp)
				requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
				ackAndAttach(t, c, root, xdg, b, serials)
				again := clientEvent[ports.WindowMapped](t, events)
				require.True(t, again.Remap)
				require.True(t, again.ColorDescription)
				s.display.Do(func() { require.True(t, s.windows[w.ID].surf().windowColorManaged) })
				return
			case "revert":
				s.display.Do(func() { content.next.color = SurfaceColor{} })
				requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
			case "unmap":
				requestProtocol(t, c, child, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
				requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
			case "destroy":
				requestProtocol(t, c, child, wayland.SurfaceRequestDestroy)
			}
			require.NoError(t, c.Roundtrip())
			require.Equal(t, ports.WindowColorDescription{ID: w.ID, PlainSDR: true}, clientEvent[ports.WindowColorDescription](t, events))
			s.display.Do(func() { s.watchStats(0) })
		})
	}
}

func TestContentSourceWaitsForCommittedLayout(t *testing.T) {
	events := make(chan ports.ClientEvent, 32)
	s, dir, _ := idleClockServerWith(t, Channels{Events: events})
	c := protocolClient(t, s, dir)
	w, root, _ := surfaceMapper(t, c, events)()
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	child, sub := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, root)
	registerProtocol(t, c, sub)
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestSetDesync)
	require.NoError(t, c.Roundtrip())
	var r, ch *surface
	s.display.Do(func() {
		r = s.windows[w.ID].surf()
		ch = r.sub.children[0]
		ch.next.color = SurfaceColor{Set: true, TF: pq, Primaries: bt2020}
	})
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, shmBufferSized(t, c, 64, 64), int32(0), int32(0))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	require.NoError(t, c.Roundtrip())
	s.display.Do(func() {
		require.True(t, ch.has)
		require.Same(t, r, r.mainContent())
		require.False(t, r.windowColorManaged)
	})
	requestProtocol(t, c, root, wayland.SurfaceRequestCommit)
	require.NoError(t, c.Roundtrip())
	require.Equal(t, ports.WindowColorDescription{ID: w.ID, PlainSDR: false}, clientEvent[ports.WindowColorDescription](t, events))
	s.display.Do(func() { require.Same(t, ch, r.mainContent()) })
}

func BenchmarkContentSourceSteadyCommit(b *testing.B) {
	r := &surface{has: true, server: &Server{}}
	r.xdg = &xdgSurface{window: &window{id: 1, mapped: true}}
	for range 100 {
		ch := &surface{has: true, sub: subState{parent: r}}
		r.sub.layout = append(r.sub.layout, childLayout{child: ch})
	}
	r.refreshWindowFacts(false)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r.refreshWindowFacts(false)
	}
}

func TestContentSourceSteadyCommitAllocations(t *testing.T) {
	r := &surface{has: true, server: &Server{}}
	r.xdg = &xdgSurface{window: &window{id: 1, mapped: true}}
	r.refreshWindowFacts(false)
	require.Zero(t, testing.AllocsPerRun(100, func() { r.refreshWindowFacts(false) }))
}
