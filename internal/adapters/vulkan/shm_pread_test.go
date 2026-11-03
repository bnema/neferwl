package vulkan

import (
	"bytes"
	"image"
	"os"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
)

func TestReadSHMRect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stride int
		rect   image.Rectangle
	}{
		{"contiguous", 12, image.Rect(0, 1, 3, 3)},
		{"padded rows", 20, image.Rect(0, 0, 3, 3)},
		{"partial damage", 20, image.Rect(1, 1, 2, 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "pool")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			const offset = 7
			src := make([]byte, offset+tc.stride*3)
			for i := range src {
				src[i] = byte(i + 1)
			}
			if _, err := f.Write(src); err != nil {
				t.Fatal(err)
			}
			fd, err := shmFD(&domain.SHMBuffer{File: f}, len(src))
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			dst, want := make([]byte, 36), make([]byte, 36)
			for y := tc.rect.Min.Y; y < tc.rect.Max.Y; y++ {
				for x := tc.rect.Min.X * 4; x < tc.rect.Max.X*4; x++ {
					want[y*12+x] = src[offset+y*tc.stride+x]
				}
			}
			if err := readSHMRect(fd, dst, offset, tc.stride, 12, tc.rect); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(dst, want) {
				t.Fatalf("got %v, want %v", dst, want)
			}
			clear(dst)
			if err := copySHMRect(fd, dst, offset, tc.stride, 12, tc.rect); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(dst, want) {
				t.Fatalf("transient copy got %v, want %v", dst, want)
			}
			// Truncate after validation: the read itself must reject EOF too.
			if err := f.Truncate(0); err != nil {
				t.Fatal(err)
			}
			if err := readSHMRect(fd, dst, offset, tc.stride, 12, tc.rect); err == nil {
				t.Fatal("accepted truncated pool")
			}
			if err := shmSize(fd, len(src)); err == nil {
				t.Fatal("accepted undersized pool")
			}
		})
	}
}

func TestSHMCopyShrunkPoolInvalid(t *testing.T) {
	// Reuse a CPU allocation as the already-created GPU destination: no Vulkan
	// operations are needed when its last frame is complete and size matches.
	dst := make([]byte, 16)
	c := &shmCopy{gpu: gpuBuffer{mapped: unsafe.Pointer(&dst[0]), size: len(dst)}, valid: true}
	key := shmKey{win: 1}
	r := &Renderer{shm: map[shmKey]*shmSurface{key: {bufs: [2]*shmCopy{c, c}}}, frame: 1}
	r.gpu = &GPU{}
	r.gpu.maxRange = 1024
	f, err := os.CreateTemp(t.TempDir(), "pool")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(16); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(8); err != nil {
		t.Fatal(err)
	}
	got, err := r.shmCopyFor(key, shmState{w: 2, h: 2, seq: 1}, nil, &domain.SHMBuffer{File: f}, 0, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.valid {
		t.Fatal("shrunk pool content marked valid")
	}
}
