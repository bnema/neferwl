package xwm

import "github.com/bnema/neferwl/internal/ports"

// Slot is the XWM's window slot: table index + 1, stable until the window is
// destroyed. 0 means none.
type Slot uint32

// WindowInfo is what the XWM learned about a managed window by the time it is
// ready to be mapped. The host copies it.
type WindowInfo struct {
	PID           int
	Floating      bool // dialog/utility/splash/fixed size
	Width, Height int  // requested size, logical (scale 1)
	Parent        Slot
	Modal         bool // _NET_WM_STATE_MODAL
	Fullscreen    bool // _NET_WM_STATE_FULLSCREEN at map
	Title, AppID  string
}

// host is the wayland server as the XWM sees it. Display goroutine only.
// All strings passed are owned by the XWM record and already deduplicated
// (only passed when changed).
type host interface {
	// Created: a managed (non-override-redirect) X window is known; the host
	// allocates a ports.WindowID and returns it. Not yet mapped.
	Created(slot Slot, xid uint32) ports.WindowID
	// Ready: properties read and MapWindow issued; the surface may still be
	// missing. Info is copied by the host.
	Ready(slot Slot, info WindowInfo)
	// Associate links the X window to the wl_surface with identity surfaceID.
	// The host maps the window on the surface's first buffer commit.
	Associate(slot Slot, surfaceID uint64)
	// Dissociate: UnmapNotify/DestroyNotify; the host emits WindowUnmapped if
	// mapped and drops the surface link. destroyed also frees the WindowID.
	Dissociate(slot Slot, destroyed bool)
	Title(slot Slot, title string) // changed only
	AppID(slot Slot, appID string) // changed only (WM_CLASS class)
	Parent(slot Slot, parent Slot) // 0 = none; after a WM_TRANSIENT_FOR change
	// Fullscreen forwards a fullscreen request (_NET_WM_STATE client message
	// or initial state); the host emits WindowFullscreenRequest.
	Fullscreen(slot Slot, on bool)
}

// fdSource is the display's event source for the X socket (*server.Source).
type fdSource interface {
	SetWritable(on bool) error
	Remove()
}
