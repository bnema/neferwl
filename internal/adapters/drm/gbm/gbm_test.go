package gbm

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// An allocator answer outside the request is refused, including an implicit
// layout (DRM_FORMAT_MOD_INVALID) that Vulkan cannot import; only a
// linear-only request takes it, as linear.
func TestCheckModifier(t *testing.T) {
	const tiled = 0x300000000606014
	for _, tc := range []struct {
		name      string
		requested []uint64
		got       uint64
		ok        bool
	}{
		{"listed", []uint64{tiled, 0}, tiled, true},
		{"linear listed", []uint64{tiled, 0}, 0, true},
		{"not listed", []uint64{tiled}, 0, false},
		{"other vendor", []uint64{tiled}, 0x200000000401b03, false},
		{"empty list", nil, 0, false},
		{"linear only takes linear", []uint64{0}, 0, true},
		{"linear only takes invalid", []uint64{0}, modInvalid, true},
		{"linear only refuses others", []uint64{0}, tiled, false},
		{"tiled refuses invalid", []uint64{tiled}, modInvalid, false},
		{"tiled or linear refuses invalid", []uint64{tiled, 0}, modInvalid, false},
		{"0 and 5 refuses invalid", []uint64{0, 5}, modInvalid, false},
	} {
		if err := checkModifier(acceptedModifiers(tc.requested), tc.got); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// Planes must be one dmabuf: the renderer imports a single memory object.
// Duplicates of one descriptor are the same inode; two files are not.
func TestCheckOneObject(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := os.CreateTemp(t.TempDir(), "b")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	dup, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dup)
	fd := int(f.Fd())
	if err := checkOneObject([]int{fd}); err != nil {
		t.Errorf("one plane: %v", err)
	}
	if err := checkOneObject([]int{fd, dup, fd}); err != nil {
		t.Errorf("shared planes: %v", err)
	}
	err = checkOneObject([]int{fd, int(g.Fd())})
	if err == nil || !strings.Contains(err.Error(), "plane 1") {
		t.Errorf("disjoint planes: %v", err)
	}
	if err := checkOneObject([]int{fd, -1}); err == nil {
		t.Error("bad descriptor accepted")
	}
}

// A closed device and bad requests are refused without touching libgbm.
func TestCreateRejectsBadRequests(t *testing.T) {
	if _, err := (*Device)(nil).Create(8, 8, 0, []uint64{0}, UseRendering); err == nil {
		t.Error("nil device accepted")
	}
	d := &Device{dev: 1}
	for _, tc := range []struct {
		w, h int
		mods []uint64
	}{{0, 8, []uint64{0}}, {8, 0, []uint64{0}}, {8, 8, nil}} {
		if _, err := d.Create(tc.w, tc.h, 0, tc.mods, UseRendering); err == nil {
			t.Errorf("%dx%d with %d modifiers accepted", tc.w, tc.h, len(tc.mods))
		}
	}
}
