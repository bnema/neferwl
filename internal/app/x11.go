package app

import "github.com/bnema/neferwl/internal/adapters/xwayland"

// lateX11 classifies X11 clients through a Display that assembly opens after
// the Wayland server is built; until then no client is X11.
type lateX11 struct{ d **xwayland.Display }

// Owns reports whether pid belongs to the opened Display's satellite.
func (l lateX11) Owns(pid int) bool { return *l.d != nil && (*l.d).Owns(pid) }
