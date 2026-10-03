package wayland

import (
	"crypto/rand"
	"slices"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgforeign"
	"github.com/bnema/purego-libwayland/server"
)

// xdg-foreign-unstable-v2 lets a client make its toplevel the child of
// another client's toplevel. The desktop portal uses it: the application
// exports its window and passes the handle over D-Bus, the portal backend
// imports it and parents its dialog to it, so the dialog floats over the
// application that asked (window.parent) instead of opening as a new tile.

// foreignExport is one zxdg_exported_v2: the handle of a toplevel.
type foreignExport struct {
	handle  string
	window  *window
	imports []*foreignImport
}

// foreignImport is one zxdg_imported_v2 and the toplevels it parented.
type foreignImport struct {
	res      *xdgforeign.ZxdgImportedV2
	export   *foreignExport // nil once invalid
	children []*window
}

func registerForeign(d *server.Display, s *Server) error {
	if err := xdgforeign.NewZxdgExporterV2Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgforeign.NewZxdgExporterV2(c, int32(v), id, foreignExporter{s})
	}); err != nil {
		return err
	}
	return xdgforeign.NewZxdgImporterV2Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgforeign.NewZxdgImporterV2(c, int32(v), id, foreignImporter{s})
	})
}

// toplevelOf is the xdg_toplevel window of a wl_surface, or nil.
func (s *Server) toplevelOf(surf *wayland.Surface) *window {
	st := s.surfaceOf(surf)
	if st == nil || st.xdg == nil || st.xdg.window == nil || st.xdg.window.toplevel == nil {
		return nil
	}
	return st.xdg.window
}

type foreignExporter struct{ s *Server }

func (foreignExporter) Destroy(*xdgforeign.ZxdgExporterV2) {}

func (e foreignExporter) ExportToplevel(r *xdgforeign.ZxdgExporterV2, id uint32, surf *wayland.Surface) {
	w := e.s.toplevelOf(surf)
	if w == nil {
		r.PostError(uint32(xdgforeign.ZxdgExporterV2ErrorInvalidSurface), "surface is not an xdg_toplevel")
		return
	}
	ex := &foreignExport{handle: rand.Text(), window: w}
	res, err := xdgforeign.NewZxdgExportedV2(r.Client(), r.Version(), id, exportedHandler{})
	if err != nil {
		return
	}
	e.s.foreignExports[ex.handle] = ex
	res.OnDestroy = func() { e.s.revokeExport(ex) }
	res.SendHandle(ex.handle)
}

type exportedHandler struct{}

func (exportedHandler) Destroy(*xdgforeign.ZxdgExportedV2) {}

type foreignImporter struct{ s *Server }

func (foreignImporter) Destroy(*xdgforeign.ZxdgImporterV2) {}

// ImportToplevel imports a handle; an unknown one is destroyed at once, as
// the protocol asks.
func (i foreignImporter) ImportToplevel(r *xdgforeign.ZxdgImporterV2, id uint32, handle string) {
	imp := &foreignImport{}
	res, err := xdgforeign.NewZxdgImportedV2(r.Client(), r.Version(), id, importedHandler{s: i.s, imp: imp})
	if err != nil {
		return
	}
	imp.res = res
	res.OnDestroy = func() { imp.invalidate(false) }
	ex := i.s.foreignExports[handle]
	if ex == nil {
		res.SendDestroyed()
		return
	}
	imp.export = ex
	ex.imports = append(ex.imports, imp)
}

type importedHandler struct {
	s   *Server
	imp *foreignImport
}

func (importedHandler) Destroy(*xdgforeign.ZxdgImportedV2) {}

// SetParentOf makes the imported toplevel the parent of the client's own
// toplevel, as xdg_toplevel.set_parent.
func (h importedHandler) SetParentOf(r *xdgforeign.ZxdgImportedV2, surf *wayland.Surface) {
	child := h.s.toplevelOf(surf)
	if child == nil || child.toplevel.Client() != r.Client() {
		r.PostError(uint32(xdgforeign.ZxdgImportedV2ErrorInvalidSurface), "surface is not an xdg_toplevel of this client")
		return
	}
	ex := h.imp.export
	if ex == nil || ex.window == child {
		return
	}
	child.setParent(ex.window)
	if !slices.Contains(h.imp.children, child) {
		h.imp.children = append(h.imp.children, child)
	}
}

// invalidate ends the relationships an import set up. send tells the client
// (destroyed) when the export, not the import, went away.
func (imp *foreignImport) invalidate(send bool) {
	for _, c := range imp.children {
		if imp.export != nil && c.parent == imp.export.window {
			c.setParent(nil)
		}
	}
	imp.children = nil
	if ex := imp.export; ex != nil {
		ex.imports = removeItem(ex.imports, imp)
		imp.export = nil
	}
	if send && imp.res.Alive() {
		imp.res.SendDestroyed()
	}
}

// revokeExport drops a handle and every relationship made with it.
func (s *Server) revokeExport(ex *foreignExport) {
	if s.foreignExports[ex.handle] == ex {
		delete(s.foreignExports, ex.handle)
	}
	for len(ex.imports) > 0 {
		ex.imports[0].invalidate(true)
	}
}

// foreignToplevelGone revokes the handles of a destroyed toplevel.
func (s *Server) foreignToplevelGone(w *window) {
	for _, ex := range s.foreignExports {
		if ex.window == w {
			s.revokeExport(ex)
		}
	}
}
