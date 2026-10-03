package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgforeign"
	xdgshell "github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// handleProxy receives zxdg_exported_v2.handle.
type handleProxy struct {
	wlturbo.BaseProxy
	handles chan string
}

func (p *handleProxy) Dispatch(e *wlturbo.Event) { p.handles <- e.String() }

// destroyedProxy receives zxdg_imported_v2.destroyed.
type destroyedProxy struct {
	wlturbo.BaseProxy
	destroyed chan struct{}
}

func (p *destroyedProxy) Dispatch(*wlturbo.Event) { p.destroyed <- struct{}{} }

// exportToplevel exports surf and returns the exported object and handle.
func exportToplevel(t *testing.T, c *wlturbo.Display, surf uint32) (uint32, string) {
	t.Helper()
	exporter := bindProtocol(t, c, "zxdg_exporter_v2")
	id := c.AllocateID()
	p := &handleProxy{handles: make(chan string, 1)}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, exporter, xdgforeign.ZxdgExporterV2RequestExportToplevel, id, surf)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-p.handles:
		return id, h
	default:
		t.Fatal("no handle")
		return 0, ""
	}
}

// importToplevel imports handle; the proxy reports destroyed.
func importToplevel(t *testing.T, c *wlturbo.Display, handle string) (uint32, *destroyedProxy) {
	t.Helper()
	importer := bindProtocol(t, c, "zxdg_importer_v2")
	id := c.AllocateID()
	p := &destroyedProxy{destroyed: make(chan struct{}, 1)}
	p.SetID(id)
	registerWireProxy(c, p)
	requestProtocol(t, c, importer, xdgforeign.ZxdgImporterV2RequestImportToplevel, id, handle)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return id, p
}

// childToplevel maps a toplevel of c whose parent is set by parent, called
// before its first buffer (as a portal dialog does).
func childToplevel(t *testing.T, c *wlturbo.Display, parent func(surf uint32)) uint32 {
	t.Helper()
	comp := bindProtocol(t, c, "wl_compositor")
	wm := bindProtocol(t, c, "xdg_wm_base")
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
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
	surf, xdg, top := c.AllocateID(), c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	registerProtocol(t, c, surf)
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, xdg, surf)
	serials := make(chan uint32, 1)
	xp := &configureProxy{serial: serials}
	xp.SetID(xdg)
	registerWireProxy(c, xp)
	registerProtocol(t, c, top)
	requestProtocol(t, c, xdg, xdgshell.SurfaceRequestGetToplevel, top)
	parent(surf)
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
	return surf
}

// A portal backend parents its dialog to another client's window through
// xdg-foreign: the dialog maps floating, with that window as its parent.
func TestForeignParentsDialog(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	app := protocolClient(t, s, dir)
	appWin, appSurf, _ := surfaceMapper(t, app, events)()
	_, handle := exportToplevel(t, app, appSurf)

	portal := protocolClient(t, s, dir)
	imported, _ := importToplevel(t, portal, handle)
	childToplevel(t, portal, func(surf uint32) {
		requestProtocol(t, portal, imported, xdgforeign.ZxdgImportedV2RequestSetParentOf, surf)
	})
	dialog := mapped(t, events, 2*time.Second)
	if !dialog.Floating || dialog.Parent != appWin.ID {
		t.Fatalf("dialog %+v, want floating over %d", dialog, appWin.ID)
	}

	// Destroying the import ends the relationship: core hears it.
	requestProtocol(t, portal, imported, xdgforeign.ZxdgImportedV2RequestDestroy)
	if err := portal.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.After(2 * time.Second); ; {
		select {
		case ev := <-events:
			if p, ok := ev.(ports.WindowParent); ok {
				if p.ID != dialog.ID || p.Parent != 0 {
					t.Fatalf("parent change %+v", p)
				}
				return
			}
		case <-deadline:
			t.Fatal("no parent change")
		}
	}
}

// An unknown handle is destroyed at once; revoking an export destroys its
// imports and drops the parent they set.
func TestForeignInvalidHandles(t *testing.T) {
	s, events, _, dir := lifecycleServer(t)
	portal := protocolClient(t, s, dir)
	_, unknown := importToplevel(t, portal, "not-a-handle")
	select {
	case <-unknown.destroyed:
	default:
		t.Fatal("unknown handle not destroyed")
	}

	app := protocolClient(t, s, dir)
	_, appSurf, _ := surfaceMapper(t, app, events)()
	exported, handle := exportToplevel(t, app, appSurf)
	imported, p := importToplevel(t, portal, handle)
	requestProtocol(t, app, exported, xdgforeign.ZxdgExportedV2RequestDestroy)
	if err := app.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := portal.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.destroyed:
	default:
		t.Fatal("import not destroyed with its export")
	}
	childToplevel(t, portal, func(surf uint32) {
		requestProtocol(t, portal, imported, xdgforeign.ZxdgImportedV2RequestSetParentOf, surf)
	})
	if dialog := mapped(t, events, 2*time.Second); dialog.Floating || dialog.Parent != 0 {
		t.Fatalf("revoked parent still applied: %+v", dialog)
	}
}

// Only toplevels can be exported.
func TestForeignExportNeedsToplevel(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	surf := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surf)
	exporter := bindProtocol(t, c, "zxdg_exporter_v2")
	id := c.AllocateID()
	registerProtocol(t, c, id)
	requestProtocol(t, c, exporter, xdgforeign.ZxdgExporterV2RequestExportToplevel, id, surf)
	expectProtocolError(t, c, exporter, uint32(xdgforeign.ZxdgExporterV2ErrorInvalidSurface))
}
