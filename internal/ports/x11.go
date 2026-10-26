package ports

// X11Clients tells whether a Wayland client process serves X11 windows
// (xwayland-satellite or its Xwayland).
type X11Clients interface{ Owns(pid int) bool }
