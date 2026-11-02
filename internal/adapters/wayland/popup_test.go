package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPopupPointerSerial(t *testing.T) {
	for _, mode := range []string{"press", "release", "stale", "foreign", "wrong-button", "arbitrary"} {
		t.Run(mode, func(t *testing.T) {
			s, events, commands, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			top, _, parent := surfaceMapper(t, c, events)()
			seat := bindVersion(t, c, "wl_seat", 8)
			registerProtocol(t, c, seat)
			pointer := c.AllocateID()
			pp := &layerPointerProxy{enters: make(chan [2]float64, 4), buttons: make(chan uint32, 4)}
			pp.SetID(pointer)
			registerWireProxy(c, pp)
			requestProtocol(t, c, seat, wayland.SeatRequestGetPointer, pointer)
			// Protocol requests and core commands arrive on independent queues.
			// Finish creating the pointer before injecting input; otherwise the
			// press can be processed before there is a pointer to receive it.
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			commands <- ports.PointerFocus{ID: top.ID}
			flushCommands(t, s, commands)
			roundtrip(t, c)
			select {
			case <-pp.enters:
			default:
				t.Fatal("missing pointer enter after focus barrier")
			}
			button := func(pressed bool, button uint32) uint32 {
				commands <- ports.PointerButtonTo{ID: top.ID, Button: button, Pressed: pressed}
				flushCommands(t, s, commands)
				roundtrip(t, c)
				select {
				case serial := <-pp.buttons:
					return serial
				default:
					t.Fatal("missing pointer button after input barrier")
					return 0
				}
			}
			serial := button(true, 0x110)
			if mode != "press" {
				b := uint32(0x110)
				if mode == "wrong-button" {
					b++
				}
				serial = button(false, b)
			}
			if mode == "stale" {
				button(true, 0x111)
			}
			if mode == "arbitrary" {
				serial += 1000
			}
			if mode == "foreign" {
				c = protocolClient(t, s, dir)
				_, _, parent = surfaceMapper(t, c, events)()
				seat = bindVersion(t, c, "wl_seat", 8)
				registerProtocol(t, c, seat)
			}
			comp := bindProtocol(t, c, "wl_compositor")
			wm := bindProtocol(t, c, "xdg_wm_base")
			buffer := shmBuffer(t, c)
			pos := c.AllocateID()
			requestProtocol(t, c, wm, xdgshell.WmBaseRequestCreatePositioner, pos)
			requestProtocol(t, c, pos, xdgshell.PositionerRequestSetSize, int32(1), int32(1))
			requestProtocol(t, c, pos, xdgshell.PositionerRequestSetAnchorRect, int32(0), int32(0), int32(1), int32(1))
			done := make(chan struct{}, 1)
			surf, xdg, serials := xdgWindow(t, c, comp, wm, buffer, func(xdg uint32) {
				popup := c.AllocateID()
				proxy := &popupDoneProxy{done: done}
				proxy.SetID(popup)
				registerWireProxy(c, proxy)
				requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetPopup, popup, parent, pos)
				requestProtocol(t, c, popup, xdgshell.PopupRequestGrab, seat, serial)
			})
			if mode != "press" && mode != "release" {
				// xdgWindow already roundtripped the rejected grab.
				select {
				case <-done:
				default:
					t.Fatal("missing popup dismissal after grab roundtrip")
				}
				if len(serials) != 0 {
					t.Fatal("rejected popup configured")
				}
				return
			}
			// The commit was roundtripped, but core events use a separate
			// forwarder: receive its event rather than dispatching more wire I/O.
			req, ok := waitEvent(t, events, 2*time.Second).(ports.PopupRequest)
			if !ok || !req.Grab || req.Parent != top.ID {
				t.Fatalf("popup request: %+v", req)
			}
			commands <- ports.ConfigurePopup{ID: req.ID, Rect: domain.Rect{W: 1, H: 1}}
			ackAndAttach(t, c, surf, xdg, buffer, serials)
			if ev, ok := waitEvent(t, events, 2*time.Second).(ports.PopupMapped); !ok || ev.ID != req.ID {
				t.Fatalf("popup mapped: %+v", ev)
			}
			if len(done) != 0 {
				t.Fatal("valid popup dismissed")
			}
		})
	}
}
