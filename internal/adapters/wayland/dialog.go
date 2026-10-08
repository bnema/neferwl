package wayland

import (
	"github.com/bnema/go-wayland-bindings/server/xdgdialog"
	"github.com/bnema/go-wayland-bindings/server/xdgshell"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// xdg-dialog: a client marks a toplevel as modal for its parent (a "Save
// as…" dialog). The parent itself still comes from xdg_toplevel.set_parent
// or xdg-foreign; core sends the focus the parent gets to its modal dialog.

func registerDialog(d *server.Display, s *Server) error {
	return xdgdialog.NewWmDialogV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = xdgdialog.NewWmDialogV1(c, int32(v), id, wmDialog{s})
	})
}

type wmDialog struct{ s *Server }

func (wmDialog) Destroy(*xdgdialog.WmDialogV1) {}

// GetXdgDialog makes the dialog object of a toplevel; a toplevel has at
// most one.
func (m wmDialog) GetXdgDialog(r *xdgdialog.WmDialogV1, id uint32, t *xdgshell.Toplevel) {
	w := m.s.toplevelWindow(t)
	if w != nil && w.dialog {
		r.PostError(uint32(xdgdialog.WmDialogV1ErrorAlreadyUsed), "toplevel already has a dialog object")
		return
	}
	res, err := xdgdialog.NewDialogV1(r.Client(), r.Version(), id, dialog{w})
	if err != nil || w == nil {
		return
	}
	w.dialog = true
	// Destroying the object ends the modal state. Once the toplevel is
	// gone the object is inert: its window stays unmapped, nothing is sent.
	res.OnDestroy = func() {
		w.dialog = false
		w.setModal(false)
	}
}

type dialog struct{ w *window }

func (dialog) Destroy(*xdgdialog.DialogV1) {}
func (d dialog) SetModal(*xdgdialog.DialogV1) {
	if d.w != nil {
		d.w.setModal(true)
	}
}
func (d dialog) UnsetModal(*xdgdialog.DialogV1) {
	if d.w != nil {
		d.w.setModal(false)
	}
}

// toplevelWindow is the window of toplevel t, nil when unknown.
func (s *Server) toplevelWindow(t *xdgshell.Toplevel) *window {
	if t == nil {
		return nil
	}
	for _, w := range s.windows {
		if w.toplevel != nil && w.toplevel.Resource == t.Resource {
			return w
		}
	}
	return nil
}

// setModal changes the modal state; core follows it once mapped (the map
// carries the state of the first frame).
func (w *window) setModal(modal bool) {
	if w.modal == modal {
		return
	}
	w.modal = modal
	if w.mapped {
		w.xdg.server.emit(ports.WindowModal{ID: w.id, Modal: modal})
	}
}
