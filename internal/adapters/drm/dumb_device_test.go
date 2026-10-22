package drm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Real-device check of the dumb buffer layer: no modeset, no atomic commit.
// It skips when no card node can be opened or makes dumb buffers.
func TestDumbBuffersOnRealDevice(t *testing.T) {
	cards, _ := filepath.Glob("/dev/dri/card*")
	if len(cards) == 0 {
		t.Skip("no /dev/dri/card* node")
	}
	for _, path := range cards {
		f, err := os.OpenFile(path, os.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Logf("%s: %v", path, err)
			continue
		}
		done := func() bool {
			defer f.Close()
			return dumbDeviceRoundTrip(t, path, kmsDevice{fd: int(f.Fd())})
		}()
		if done {
			return
		}
	}
	t.Skip("no card node makes dumb buffers for this user")
}

// dumbDeviceRoundTrip runs the buffer life cycle on k and reports false
// when the card cannot do it for a reason unrelated to the code under test
// (the permission or the driver).
func dumbDeviceRoundTrip(t *testing.T, path string, k kmsDevice) bool {
	size := cursorSize(k.fd)
	var imgs [2]dumbBuffer
	var mems [2][]byte
	var fbs [2]uint32
	cleanup := func() {
		for i := range imgs {
			if fbs[i] != 0 {
				if err := k.rmFB(fbs[i]); err != nil {
					t.Errorf("rmfb: %v", err)
				}
			}
			if mems[i] != nil {
				if err := k.unmapDumb(mems[i]); err != nil {
					t.Errorf("munmap: %v", err)
				}
			}
			if imgs[i].handle != 0 {
				if err := k.destroyDumb(imgs[i].handle); err != nil {
					t.Errorf("destroy dumb: %v", err)
				}
			}
		}
	}
	defer cleanup()
	for i := range imgs {
		b, err := k.createDumb(size, size)
		if err != nil {
			if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
				t.Logf("%s: create dumb: %v", path, err)
				return false
			}
			t.Fatalf("%s: create dumb: %v", path, err)
		}
		imgs[i] = b
		if int(b.pitch) < size*4 || b.size < uint64(int(b.pitch)*size) {
			t.Fatalf("%s: dumb %dx%d pitch %d size %d", path, size, size, b.pitch, b.size)
		}
		if mems[i], err = k.mapDumb(b); err != nil {
			t.Fatalf("%s: map dumb: %v", path, err)
		}
		if fbs[i], err = k.addFBHandle(b, size, size, fourccARGB); err != nil {
			t.Fatalf("%s: add fb2 ARGB8888: %v", path, err)
		}
		t.Logf("%s: dumb buffer %d: %dx%d pitch %d size %d fb %d", path, i, size, size, b.pitch, b.size, fbs[i])
	}
	// Write a pattern through the shared mapping; each buffer keeps its own.
	for i, mem := range mems {
		d := &dumbCursor{k: k, size: size}
		d.img[i] = dumbImage{pitch: int(imgs[i].pitch), mem: mem}
		px := bytes.Repeat([]byte{byte(0x10 + i), 0x20, 0x30, 0xff}, 3*2)
		if err := d.write(i, px, 3, 2, 0); err != nil {
			t.Fatal(err)
		}
	}
	for i, mem := range mems {
		pitch := int(imgs[i].pitch)
		want := []byte{byte(0x10 + i), 0x20, 0x30, 0xff}
		for y := range 2 {
			if got := mem[y*pitch : y*pitch+4]; !bytes.Equal(got, want) {
				t.Fatalf("%s: buffer %d row %d read back %v, want %v", path, i, y, got, want)
			}
			if got := mem[y*pitch+3*4]; got != 0 {
				t.Fatalf("%s: buffer %d row %d not cleared after the image", path, i, y)
			}
		}
		if mem[2*pitch] != 0 || mem[(size-1)*pitch+(size-1)*4] != 0 {
			t.Fatalf("%s: buffer %d not cleared below the image", path, i)
		}
	}
	return true
}
