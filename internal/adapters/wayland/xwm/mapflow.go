package xwm

import "bnema.dev/xturbo"

// beginMap starts the map flow of a managed window: properties, geometry,
// MapWindow. Also called by Attach for windows mapped before it.
func (x *XWM) beginMap(s Slot) { _ = s }

func (x *XWM) onMapRequest(ev *xturbo.Event)    { _ = ev }
func (x *XWM) onMapNotify(ev *xturbo.Event)     { _ = ev }
func (x *XWM) onUnmapNotify(ev *xturbo.Event)   { _ = ev }
func (x *XWM) onClientMessage(ev *xturbo.Event) { _ = ev }

func (x *XWM) onGeomReply(s Slot, rec *record, r *xturbo.Reply)      { _, _, _ = s, rec, r }
func (x *XWM) onClientIdsReply(s Slot, rec *record, r *xturbo.Reply) { _, _, _ = s, rec, r }

// SetSerial records the serial of a wl_surface (xwayland_surface_v1.set_serial).
func (x *XWM) SetSerial(surfaceID uint64, serial uint64) { _, _ = surfaceID, serial }

// SurfaceGone forgets a destroyed wl_surface.
func (x *XWM) SurfaceGone(surfaceID uint64) { _ = surfaceID }
