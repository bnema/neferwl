package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgdialog"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// A modal dialog maps with its state, and later changes reach core. A
// second dialog object for the same toplevel is a protocol error.
func TestDialogModal(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	parent, _, parentXDG := surfaceMapper(t, c, events)()
	// surfaceMapper allocates the toplevel right after its xdg_surface.
	parentTop := parentXDG + 1

	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	dialogs := bindProtocol(t, c, "xdg_wm_dialog_v1")
	fd, err := unix.MemfdCreate("dialog-buffer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(1), int32(1), int32(4), uint32(0))

	// The dialog toplevel: modal before its first frame.
	surf, xdg, top, dlg := c.AllocateID(), c.AllocateID(), c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 1)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	registerWireProxy(c, xp)
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	registerProtocol(t, c, dlg)
	requestProtocol(t, c, dialogs, xdgdialog.WmDialogV1RequestGetXdgDialog, dlg, top)
	requestProtocol(t, c, dlg, xdgdialog.DialogV1RequestSetModal)
	requestProtocol(t, c, top, xdgshell.ToplevelRequestSetParent, parentTop)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case serial := <-serials:
		requestProtocol(t, c, xdg, xdgshell.SurfaceRequestAckConfigure, serial)
	default:
		t.Fatal("missing configure")
	}
	requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	d := mapped(t, events, 2*time.Second)
	if !d.Modal || d.Parent != parent.ID || !d.Floating {
		t.Fatalf("dialog %+v, want modal over %d", d, parent.ID)
	}

	modal := func(want bool) {
		t.Helper()
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.After(2 * time.Second); ; {
			select {
			case ev := <-events:
				if m, ok := ev.(ports.WindowModal); ok {
					if m.ID != d.ID || m.Modal != want {
						t.Fatalf("modal change %+v, want %v", m, want)
					}
					return
				}
			case <-deadline:
				t.Fatal("no modal change")
			}
		}
	}
	requestProtocol(t, c, dlg, xdgdialog.DialogV1RequestUnsetModal)
	modal(false)
	requestProtocol(t, c, dlg, xdgdialog.DialogV1RequestSetModal)
	modal(true)
	// Destroying the object ends the modal state.
	requestProtocol(t, c, dlg, xdgdialog.DialogV1RequestDestroy)
	modal(false)

	// A toplevel gets one dialog object at a time.
	first, second := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, first)
	registerProtocol(t, c, second)
	requestProtocol(t, c, dialogs, xdgdialog.WmDialogV1RequestGetXdgDialog, first, top)
	requestProtocol(t, c, dialogs, xdgdialog.WmDialogV1RequestGetXdgDialog, second, top)
	expectProtocolError(t, c, dialogs, uint32(xdgdialog.WmDialogV1ErrorAlreadyUsed))
}
