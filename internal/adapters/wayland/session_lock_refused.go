package wayland

import "github.com/bnema/purego-libwayland/protocol/extsessionlock"

// A constructor pipelined before finished must still create its new object,
// but a refused lock never assigns a wl_surface role or changes active focus.
type refusedLockSurface struct{}

func (refusedLockSurface) Destroy(*extsessionlock.ExtSessionLockSurfaceV1) {}
func (refusedLockSurface) AckConfigure(r *extsessionlock.ExtSessionLockSurfaceV1, _ uint32) {
	r.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorInvalidSerial), "refused lock has no configure")
}
