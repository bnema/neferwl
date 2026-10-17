package wayland

import (
	"slices"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// Drag and drop (wl_data_device.start_drag). A client starts a drag with the
// serial of its current press; wayland validates it and tells core
// (ports.DataDragStarted). Core owns the pointer from then on: it names the
// surface under the cursor (DataDragFocus, DataDragMotion), the release
// (DataDragDrop) and the Escape key (DataDragCancel). Wayland speaks the
// data device protocol to the source and the target: enter, motion, leave
// and drop with an offer, and the action negotiation the wlroots way. The
// icon is a client surface with its own window ID; core draws it at the
// cursor from the geometry sent with DataDragIcon.
//
// Core ends a drag with DataDragDrop or DataDragCancel and is not told
// (the lock resets both sides). DataDragEnded tells core about an end it
// did not cause: the source or the origin client died.

// dndAllActions is every wl_data_device_manager.dnd_action bit.
const dndAllActions = uint32(wayland.DataDeviceManagerDndActionCopy | wayland.DataDeviceManagerDndActionMove | wayland.DataDeviceManagerDndActionAsk)

// dndState is the drag in progress.
type dndState struct {
	// src is the source; nil for a drag internal to the origin client.
	src    *clipSource
	client server.Client
	// icon is the icon surface (nil: none or destroyed); iconID is the
	// window ID core knows it by (0: none).
	icon   *surface
	iconID ports.WindowID
	// iconX, iconY is the icon's top left corner relative to the cursor:
	// the offsets of its commits add up. iconSent is the last geometry sent.
	iconX, iconY int
	iconSent     ports.DataDragIcon
	target       *dndTarget
}

const (
	targetActive = iota
	targetDropped
	targetDone
)

// dndTarget is the surface a drag entered. Its offers (one per data device
// of the client) share the negotiation. After the drop it lives on until
// the target finishes or destroys its offers.
type dndTarget struct {
	d       *dndState
	surface *wayland.Surface
	devices []*wayland.DataDevice
	offers  []*dndOffer
	// accepted is the MIME type the target accepted; action the action
	// chosen for the source.
	accepted string
	action   uint32
	state    int
	// legacy: no device speaks wl_data_offer v3, so the accepted MIME type
	// alone allows the drop and nothing finishes it.
	legacy bool
}

// dndOffer is a wl_data_offer of a drag.
type dndOffer struct {
	server *Server
	t      *dndTarget
	res    *wayland.DataOffer
	// actions and preferred are the target's set_actions; sent is the last
	// action told to this offer.
	actions, preferred, sent uint32
}

// srcActions is what the source allows; a source that never said (a
// pre-v3 client) only copies.
func (src *clipSource) srcActions() uint32 {
	if src.actionsSet {
		return src.actions
	}
	return uint32(wayland.DataDeviceManagerDndActionCopy)
}

// chooseAction picks the action like wlroots: the preferred one if both
// allow it, else the first of copy, move, ask.
func chooseAction(avail, preferred uint32) uint32 {
	if avail == 0 {
		return 0
	}
	if preferred&avail != 0 {
		return preferred
	}
	for _, a := range [...]uint32{uint32(wayland.DataDeviceManagerDndActionCopy), uint32(wayland.DataDeviceManagerDndActionMove), uint32(wayland.DataDeviceManagerDndActionAsk)} {
		if avail&a != 0 {
			return a
		}
	}
	return 0
}

// addDataDevice tracks a client's data devices, the drag targets.
func (s *Server) addDataDevice(c server.Client, dev *wayland.DataDevice) {
	if s.dataDevices == nil {
		s.dataDevices = map[server.Client][]*wayland.DataDevice{}
	}
	s.dataDevices[c] = append(s.dataDevices[c], dev)
	dev.OnDestroy = func() {
		// Copy on write: a drag may be iterating the old list.
		list := slices.DeleteFunc(slices.Clone(s.dataDevices[c]), func(x *wayland.DataDevice) bool { return x == dev })
		if len(list) == 0 {
			delete(s.dataDevices, c)
			// An internal drag has nobody left to tell.
			if d := s.dnd; d != nil && d.client == c && d.src == nil {
				s.dndEnd(d, false, true)
			}
		} else {
			s.dataDevices[c] = list
		}
	}
}

// startDrag handles wl_data_device.start_drag.
func (s *Server) startDrag(r *wayland.DataDevice, srcRes *wayland.DataSource, origin, icon *wayland.Surface, serial uint32) {
	var cs *clipSource
	if srcRes != nil {
		if cs = s.dataSources[srcRes.Resource]; cs == nil {
			return
		}
	}
	var iconSurf *surface
	if icon != nil {
		if iconSurf = s.surfaces[icon.Resource]; iconSurf == nil {
			return
		}
		if iconSurf.kind != roleNone && iconSurf.kind != roleDragIcon {
			r.PostError(uint32(wayland.DataDeviceErrorRole), "icon surface already has another role")
			return
		}
	}
	client := r.Client()
	valid := !s.protected() && s.dnd == nil && origin != nil && origin.Client() == client &&
		s.seat.press != 0 && serial == s.seat.press && s.seat.pressClient == client
	if cs != nil && (cs.used || !valid) {
		// A used source is the selection, a drag source or already
		// cancelled: only a source nobody holds is cancelled again.
		if !s.sourceBusy(cs) {
			cs.used = true
			if cs.alive() {
				cs.cancel()
			}
		}
		return
	}
	if !valid {
		return
	}
	d := &dndState{src: cs, client: client, icon: iconSurf}
	if cs != nil {
		cs.used = true
	}
	if iconSurf != nil {
		d.iconID = s.nextWindow
		s.nextWindow++
		iconSurf.kind = roleDragIcon
		iconSurf.role = func(bool) { s.dndIconRole(d, iconSurf) }
	}
	s.dnd = d
	s.log.Info().Uint64("icon", uint64(d.iconID)).Bool("source", cs != nil).Msg("data drag started")
	s.emit(ports.DataDragStarted{Icon: d.iconID})
	if iconSurf != nil && iconSurf.has {
		// The icon was committed before the drag: show what it holds.
		iconSurf.committed = damage{full: true}
		iconSurf.redraw()
		s.dndIconSend(d)
	}
}

// sourceBusy reports whether src is the selection, the source of the drag
// or of a drop waiting for its finish.
func (s *Server) sourceBusy(src *clipSource) bool {
	if s.selections[selClipboard] == src || s.dnd != nil && s.dnd.src == src {
		return true
	}
	return slices.ContainsFunc(s.dndDrops, func(t *dndTarget) bool { return t.d.src == src })
}

// dndIconRole runs on each commit of the icon surface, and on its
// destruction.
func (s *Server) dndIconRole(d *dndState, icon *surface) {
	if !icon.destroyed || d.icon != icon {
		return
	}
	// The drag goes on without icon.
	id := d.iconID
	d.icon, d.iconID = nil, 0
	s.emit(ports.DataDragIcon{ID: id})
	s.emitContent(ports.SurfaceContent{ID: id}, damage{full: true})
}

// dndIconCommit follows a commit of the active icon: its offsets add up
// and its geometry goes to core.
func (s *Server) dndIconCommit(icon *surface, dx, dy int32) {
	d := s.dnd
	if d == nil || d.icon != icon {
		return
	}
	d.iconX += int(dx)
	d.iconY += int(dy)
	s.dndIconSend(d)
}

func (s *Server) dndIconSend(d *dndState) {
	ic := ports.DataDragIcon{ID: d.iconID, X: d.iconX, Y: d.iconY}
	if d.icon != nil && d.icon.has {
		ic.W, ic.H = d.icon.content.LogicalW, d.icon.content.LogicalH
	}
	if ic != d.iconSent {
		d.iconSent = ic
		s.emit(ic)
	}
}

// applyDrag applies the commands core sends during a drag.
func (s *Server) applyDrag(cmd ports.ClientCommand) {
	d := s.dnd
	if d == nil {
		return
	}
	switch c := cmd.(type) {
	case ports.DataDragCancel:
		s.dndEnd(d, true, false)
	case ports.DataDragDrop:
		s.dndDrop(d)
	case ports.DataDragFocus:
		s.dndFocus(d, c)
	case ports.DataDragMotion:
		if t := d.target; t != nil && !s.protected() {
			surf, _, x, y := s.pointerSurface(c.ID, c.X, c.Y)
			if surf == nil || surf.Resource != t.surface.Resource {
				return
			}
			for _, dev := range t.devices {
				if dev.Resource.Alive() {
					dev.SendMotion(wireMsec(c.Time), server.FixedFromFloat(x), server.FixedFromFloat(y))
				}
			}
		}
	}
}

// dndFocus moves the drag to the surface core names: the old target gets
// leave, the new one enter with an offer.
func (s *Server) dndFocus(d *dndState, c ports.DataDragFocus) {
	s.dndLeave(d)
	if c.ID == 0 || s.protected() {
		return
	}
	surf, _, x, y := s.pointerSurface(c.ID, c.X, c.Y)
	if surf == nil {
		return
	}
	client := surf.Client()
	// Without a source the drag is internal to the origin client.
	if d.src == nil && client != d.client {
		return
	}
	devices := s.dataDevices[client]
	if len(devices) == 0 {
		return
	}
	t := &dndTarget{d: d, surface: surf, devices: devices, legacy: true}
	d.target = t
	for _, dev := range devices {
		if !dev.Resource.Alive() {
			continue
		}
		if dev.Version() >= 3 {
			t.legacy = false
		}
		var offer *wayland.DataOffer
		if d.src != nil {
			o := &dndOffer{server: s, t: t}
			res, err := wayland.NewDataOffer(dev.Client(), dev.Version(), 0, o)
			if err != nil {
				continue
			}
			o.res = res
			res.OnDestroy = func() { s.dndOfferGone(o) }
			t.offers = append(t.offers, o)
			dev.SendDataOffer(res)
			for _, mime := range d.src.mimes {
				res.SendOffer(mime)
			}
			if dev.Version() >= 3 {
				res.SendSourceActions(d.src.srcActions())
			}
			offer = res
		}
		s.serial++
		dev.SendEnter(s.serial, surf, server.FixedFromFloat(x), server.FixedFromFloat(y), offer)
	}
}

// dndLeave sends leave to the target of the drag and forgets it: its
// offers become inert.
func (s *Server) dndLeave(d *dndState) {
	t := d.target
	if t == nil {
		return
	}
	d.target = nil
	t.state = targetDone
	for _, dev := range t.devices {
		if dev.Resource.Alive() {
			dev.SendLeave()
		}
	}
	s.dndResetSource(t)
}

// dndResetSource tells the source nothing accepts or acts any more.
func (s *Server) dndResetSource(t *dndTarget) {
	src := t.d.src
	if src == nil || !src.alive() {
		return
	}
	if t.accepted != "" {
		src.res.SendTarget("")
	}
	if t.action != 0 && src.res.Version() >= 3 {
		src.res.SendAction(0)
	}
	t.accepted, t.action = "", 0
}

// dndDrop handles the release of the drag button: a target that accepted
// and agreed on an action gets drop; else the drag is cancelled.
func (s *Server) dndDrop(d *dndState) {
	t := d.target
	switch {
	case t == nil:
		s.dndEnd(d, true, false)
	case d.src == nil:
		// Internal drag: the origin client decides alone.
		d.target = nil
		t.state = targetDone
		s.dndSendDrop(t)
		s.dndEnd(d, false, false)
	case t.accepted == "" || t.action == 0 && !t.legacy:
		s.dndEnd(d, true, false)
	default:
		d.target = nil
		t.state = targetDropped
		if t.legacy {
			// A pre-v3 target copies and nothing finishes its transfer.
			if t.action == 0 {
				t.action = uint32(wayland.DataDeviceManagerDndActionCopy)
				if d.src.alive() && d.src.res.Version() >= 3 {
					d.src.res.SendAction(t.action)
				}
			}
		} else {
			s.dndDrops = append(s.dndDrops, t)
		}
		s.dndSendDrop(t)
		if d.src.alive() && d.src.res.Version() >= 3 {
			d.src.res.SendDndDropPerformed()
		}
		s.log.Info().Str("mime", t.accepted).Uint32("action", t.action).Msg("data drag dropped")
		s.dndEnd(d, false, false)
	}
}

func (s *Server) dndSendDrop(t *dndTarget) {
	for _, dev := range t.devices {
		if dev.Resource.Alive() {
			dev.SendDrop()
		}
	}
}

// dndEnd ends the drag d: the target gets leave, the source cancelled if
// cancel (it is not once dropped), the icon goes. ended tells core about an
// end it did not cause.
func (s *Server) dndEnd(d *dndState, cancel, ended bool) {
	s.dndLeave(d)
	if cancel && d.src != nil && d.src.alive() {
		d.src.cancel()
	}
	if s.dnd == d {
		s.dnd = nil
	}
	if d.icon != nil {
		d.icon.role = nil
		d.icon = nil
	}
	if d.iconID != 0 {
		s.emitContent(ports.SurfaceContent{ID: d.iconID}, damage{full: true})
		d.iconID = 0
	}
	if ended {
		s.log.Info().Msg("data drag ended")
		s.emit(ports.DataDragEnded{})
	}
}

// dndSourceGone ends the drags of a destroyed source.
func (s *Server) dndSourceGone(src *clipSource) {
	if d := s.dnd; d != nil && d.src == src {
		s.dndEnd(d, false, true)
	}
	for _, t := range slices.Clone(s.dndDrops) {
		if t.d.src == src {
			s.dndDropDone(t)
		}
	}
}

// dndDropDone forgets a dropped target.
func (s *Server) dndDropDone(t *dndTarget) {
	t.state = targetDone
	s.dndDrops = slices.DeleteFunc(s.dndDrops, func(x *dndTarget) bool { return x == t })
}

// dndSurfaceGone clears the drag target when its surface is destroyed.
func (s *Server) dndSurfaceGone(surf *surface) {
	if d := s.dnd; d != nil && d.target != nil && surf.wl != nil && d.target.surface.Resource == surf.wl.Resource {
		s.dndLeave(d)
	}
}

// dndCancelAll cancels the drag and the drops waiting for their finish: the
// session is protected.
func (s *Server) dndCancelAll() {
	if d := s.dnd; d != nil {
		s.dndEnd(d, true, false)
	}
	for _, t := range slices.Clone(s.dndDrops) {
		if src := t.d.src; src != nil && src.alive() {
			src.cancel()
		}
		s.dndDropDone(t)
	}
}

// dndOfferGone runs when an offer of a drag is destroyed: a dropped target
// whose offers are all gone without finish cancels the source.
func (s *Server) dndOfferGone(o *dndOffer) {
	t := o.t
	t.offers = slices.DeleteFunc(t.offers, func(x *dndOffer) bool { return x == o })
	if t.state == targetDropped && !t.legacy && len(t.offers) == 0 {
		if src := t.d.src; src != nil && src.alive() {
			src.cancel()
		}
		s.dndDropDone(t)
	}
}

// update recomputes the action after the target or the source changed
// something, and tells both when it changed.
func (o *dndOffer) update() {
	t, src := o.t, o.t.d.src
	if t.state == targetDone || src == nil || !src.alive() {
		return
	}
	action := chooseAction(o.actions&src.srcActions(), o.preferred)
	if action != t.action {
		t.action = action
		if src.res.Version() >= 3 {
			src.res.SendAction(action)
		}
	}
	if action != o.sent {
		o.sent = action
		if o.res.Version() >= 3 {
			o.res.SendAction(action)
		}
	}
}

func (o *dndOffer) Accept(_ *wayland.DataOffer, _ uint32, mime string) {
	t := o.t
	if t.state != targetActive || !t.d.src.alive() {
		return
	}
	t.accepted = mime
	t.d.src.res.SendTarget(mime)
}

func (o *dndOffer) Receive(_ *wayland.DataOffer, mime string, fd int) {
	defer unix.Close(fd)
	src := o.t.d.src
	if o.server.protected() || o.t.state == targetDone || !src.alive() {
		return
	}
	if slices.Contains(src.mimes, mime) {
		src.send(mime, fd)
	}
}

func (*dndOffer) Destroy(*wayland.DataOffer) {}

func (o *dndOffer) Finish(r *wayland.DataOffer) {
	t := o.t
	switch t.state {
	case targetDone:
		return
	case targetActive:
		r.PostError(uint32(wayland.DataOfferErrorInvalidFinish), "finish before the drop")
		return
	}
	if t.action == 0 || t.action == uint32(wayland.DataDeviceManagerDndActionAsk) {
		r.PostError(uint32(wayland.DataOfferErrorInvalidFinish), "finish without a final action")
		return
	}
	o.server.dndDropDone(t)
	if src := t.d.src; src.alive() && src.res.Version() >= 3 {
		src.res.SendDndFinished()
	}
}

func (o *dndOffer) SetActions(r *wayland.DataOffer, actions, preferred uint32) {
	if actions&^dndAllActions != 0 {
		r.PostError(uint32(wayland.DataOfferErrorInvalidActionMask), "invalid action mask")
		return
	}
	if preferred != 0 && (preferred&(preferred-1) != 0 || preferred&actions == 0) {
		r.PostError(uint32(wayland.DataOfferErrorInvalidAction), "invalid preferred action")
		return
	}
	o.actions, o.preferred = actions, preferred
	o.update()
}
