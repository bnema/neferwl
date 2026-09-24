package drm

import (
	"testing"
	"unsafe"
)

// The ioctl numbers encode the struct size; they must match the kernel ABI.
func TestCursorABI(t *testing.T) {
	if s := unsafe.Sizeof(modeCursor2{}); s != 36 || ioctlCursor2>>16&0x3fff != 36 {
		t.Fatalf("drm_mode_cursor2 size %d", s)
	}
	if s := unsafe.Sizeof(getCap{}); s != 16 || ioctlGetCap>>16&0x3fff != 16 {
		t.Fatalf("drm_get_cap size %d", s)
	}
}
