package wayland

import (
	"testing"

	cm "github.com/bnema/go-wayland-bindings/server/colormanagement"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

func TestColorManagerErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   uint32
		code uint32
	}{
		{"ICC", cm.WpColorManagerV1RequestCreateIccCreator, 0},
		{"scRGB", cm.WpColorManagerV1RequestCreateWindowsScrgb, 0},
		{"duplicate surface", cm.WpColorManagerV1RequestGetSurface, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, _ := colorServer(t)
			manager := bindVersion(t, c, "wp_color_manager_v1", 2)
			registerProtocol(t, c, manager)
			var wl uint32
			if tc.name == "duplicate surface" {
				comp := bindProtocol(t, c, "wl_compositor")
				wl = c.AllocateID()
				requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
				registerProtocol(t, c, wl)
				control := c.AllocateID()
				requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurface, control, wl)
				registerProtocol(t, c, control)
			}
			id := c.AllocateID()
			if wl != 0 {
				requestProtocol(t, c, manager, tc.op, id, wl)
			} else {
				requestProtocol(t, c, manager, tc.op, id)
			}
			expectProtocolError(t, c, manager, tc.code)
		})
	}
}
func TestColorSurfaceErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   uint32
		intent uint32
		inert  bool
	}{
		{"intent", 0, 42, false}, {"description", 1, 0, false}, {"inert", 2, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, _ := colorServer(t)
			manager := bindVersion(t, c, "wp_color_manager_v1", 2)
			registerProtocol(t, c, manager)
			comp := bindProtocol(t, c, "wl_compositor")
			wl := c.AllocateID()
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, wl)
			registerProtocol(t, c, wl)
			control := c.AllocateID()
			requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetSurface, control, wl)
			registerProtocol(t, c, control)
			if tc.inert {
				requestProtocol(t, c, wl, wayland.SurfaceRequestDestroy)
				requestProtocol(t, c, control, cm.WpColorManagementSurfaceV1RequestUnsetImageDescription)
			} else {
				var global uint32
				for name, g := range c.Registry().GetGlobals() {
					if g.Interface == "wl_output" {
						global = name
					}
				}
				out, err := bindWireID(c, global, "wl_output", 4)
				if err != nil {
					t.Fatal(err)
				}
				registerProtocol(t, c, out)
				output := c.AllocateID()
				requestProtocol(t, c, manager, cm.WpColorManagerV1RequestGetOutput, output, out)
				registerProtocol(t, c, output)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				if tc.name == "description" {
					s.display.Do(func() { s.setOutputs(ports.SetOutputs{}) })
					if err := c.Roundtrip(); err != nil {
						t.Fatal(err)
					}
				}
				img := c.AllocateID()
				requestProtocol(t, c, output, cm.WpColorManagementOutputV1RequestGetImageDescription, img)
				registerProtocol(t, c, img)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				requestProtocol(t, c, control, cm.WpColorManagementSurfaceV1RequestSetImageDescription, img, tc.intent)
			}
			expectProtocolError(t, c, control, tc.code)
		})
	}
}
