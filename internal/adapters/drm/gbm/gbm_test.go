package gbm

import (
	"os"
	"path/filepath"
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

func TestReopenDeviceIndependentDescription(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "device")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := reopenDevice(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var a, b unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &a); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(fd, &b); err != nil {
		t.Fatal(err)
	}
	if a.Dev != b.Dev || a.Ino != b.Ino {
		t.Fatal("reopened a different node")
	}
	if _, err := unix.Seek(fd, 17, 0); err != nil {
		t.Fatal(err)
	}
	if offset, err := f.Seek(0, 1); err != nil || offset != 0 {
		t.Fatalf("shared file offset: %d, %v", offset, err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("missing CLOEXEC: %d, %v", flags, err)
	}
}

func TestReopenDevicePrefersRenderNode(t *testing.T) {
	primary, err := os.CreateTemp(t.TempDir(), "card")
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	sysChar, devDRI := t.TempDir(), t.TempDir()
	// A regular file has rdev 0:0; discovery still exercises the real fstat.
	drmDir := filepath.Join(sysChar, "0:0", "device", "drm")
	if err := os.MkdirAll(filepath.Join(drmDir, "renderD128"), 0700); err != nil {
		t.Fatal(err)
	}
	renderPath := filepath.Join(devDRI, "renderD128")
	render, err := os.Create(renderPath)
	if err != nil {
		t.Fatal(err)
	}
	defer render.Close()
	fd, err := reopenDeviceAt(int(primary.Fd()), sysChar, devDRI)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var want, got unix.Stat_t
	if err := unix.Fstat(int(render.Fd()), &want); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(fd, &got); err != nil {
		t.Fatal(err)
	}
	if want.Dev != got.Dev || want.Ino != got.Ino {
		t.Fatal("did not open render node")
	}
	if _, err := unix.Seek(fd, 17, 0); err != nil {
		t.Fatal(err)
	}
	if offset, err := render.Seek(0, 1); err != nil || offset != 0 {
		t.Fatalf("shared file offset: %d, %v", offset, err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("missing CLOEXEC: %d, %v", flags, err)
	}
	if err := os.Remove(renderPath); err != nil {
		t.Fatal(err)
	}
	if fd, err := reopenDeviceAt(int(primary.Fd()), sysChar, devDRI); err == nil {
		unix.Close(fd)
		t.Fatal("fell back to primary despite a present render node")
	}
}
