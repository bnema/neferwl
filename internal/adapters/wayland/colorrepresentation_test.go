package wayland

import (
	"testing"

	cr "github.com/bnema/purego-libwayland/protocol/colorrepresentation"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

func TestColorRepresentationProtocol(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindProtocol(t, c, "wp_color_representation_manager_v1")
	events := watchColor(c, manager)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 9 || uint32(events.events[len(events.events)-1]) != cr.WpColorRepresentationManagerV1EventDone {
		t.Fatalf("capabilities %v", events.events)
	}
	comp := bindProtocol(t, c, "wl_compositor")
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	obj := c.AllocateID()
	requestProtocol(t, c, manager, cr.WpColorRepresentationManagerV1RequestGetSurface, obj, wl)
	registerProtocol(t, c, obj)
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, uint32(coefficient2020), uint32(rangeLimited))
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestSetChromaLocation, uint32(cr.WpColorRepresentationSurfaceV1ChromaLocationType0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var before, after surfaceRepresentation
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				before = surf.representation
			}
		}
	})
	if before != (surfaceRepresentation{}) {
		t.Fatalf("applied before commit: %+v", before)
	}
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				after = surf.representation
			}
		}
	})
	if after.coefficients != coefficient2020 || after.rangeValue != rangeLimited || after.chroma != 1 {
		t.Fatalf("commit %+v", after)
	}
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestDestroy)
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				after = surf.representation
			}
		}
	})
	if after != (surfaceRepresentation{}) {
		t.Fatalf("destroy %+v", after)
	}
}

func TestColorRepresentationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   uint32
		args []any
		code uint32
	}{
		{"alpha", cr.WpColorRepresentationSurfaceV1RequestSetAlphaMode, []any{uint32(2)}, uint32(cr.WpColorRepresentationSurfaceV1ErrorAlphaMode)},
		{"coefficients", cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, []any{uint32(8), uint32(1)}, uint32(cr.WpColorRepresentationSurfaceV1ErrorCoefficients)},
		{"range", cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, []any{uint32(coefficient2020), uint32(3)}, uint32(cr.WpColorRepresentationSurfaceV1ErrorCoefficients)},
		{"chroma", cr.WpColorRepresentationSurfaceV1RequestSetChromaLocation, []any{uint32(2)}, uint32(cr.WpColorRepresentationSurfaceV1ErrorChromaLocation)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, _ := colorServer(t)
			manager := bindProtocol(t, c, "wp_color_representation_manager_v1")
			registerProtocol(t, c, manager)
			comp := bindProtocol(t, c, "wl_compositor")
			wl := c.AllocateID()
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
			registerProtocol(t, c, wl)
			obj := c.AllocateID()
			requestProtocol(t, c, manager, cr.WpColorRepresentationManagerV1RequestGetSurface, obj, wl)
			registerProtocol(t, c, obj)
			requestProtocol(t, c, obj, tc.op, tc.args...)
			expectProtocolError(t, c, obj, tc.code)
		})
	}
}

// A queued commit snapshots representation; later requests must not rewrite it.
func TestColorRepresentationQueuedCommit(t *testing.T) {
	s, c, _ := colorServer(t)
	manager := bindProtocol(t, c, "wp_color_representation_manager_v1")
	registerProtocol(t, c, manager)
	comp := bindProtocol(t, c, "wl_compositor")
	registerProtocol(t, c, comp)
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	obj := c.AllocateID()
	requestProtocol(t, c, manager, cr.WpColorRepresentationManagerV1RequestGetSurface, obj, wl)
	registerProtocol(t, c, obj)
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, uint32(coefficient709), uint32(rangeLimited))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() == wl {
				surf.queueUpdate()
			}
		}
	})
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, uint32(coefficient2020), uint32(rangeFull))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		for res, surf := range s.surfaces {
			if res.ID() != wl {
				continue
			}
			// The pacer may have already applied the queued update.
			if len(surf.queue) == 1 {
				if surf.queue[0].representation.coefficients != coefficient709 {
					t.Errorf("queued representation: %+v", surf.queue[0].representation)
				}
				u := surf.queue[0]
				surf.queue = nil
				surf.applyUpdate(u)
			}
			if surf.representation.coefficients != coefficient709 || surf.pendingRepresentation.coefficients != coefficient2020 {
				t.Errorf("applied=%+v pending=%+v", surf.representation, surf.pendingRepresentation)
			}
		}
	})
}

func TestColorRepresentationRejectsRGBWithYUVCoefficients(t *testing.T) {
	_, c, _ := colorServer(t)
	manager := bindProtocol(t, c, "wp_color_representation_manager_v1")
	registerProtocol(t, c, manager)
	comp := bindProtocol(t, c, "wl_compositor")
	registerProtocol(t, c, comp)
	wl := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
	registerProtocol(t, c, wl)
	obj := c.AllocateID()
	requestProtocol(t, c, manager, cr.WpColorRepresentationManagerV1RequestGetSurface, obj, wl)
	registerProtocol(t, c, obj)
	buf := shmBuffer(t, c)
	requestProtocol(t, c, wl, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	requestProtocol(t, c, obj, cr.WpColorRepresentationSurfaceV1RequestSetCoefficientsAndRange, uint32(coefficient709), uint32(rangeLimited))
	requestProtocol(t, c, wl, wayland.SurfaceRequestCommit)
	expectProtocolError(t, c, obj, uint32(cr.WpColorRepresentationSurfaceV1ErrorPixelFormat))
}
