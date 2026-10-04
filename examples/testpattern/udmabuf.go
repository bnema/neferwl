package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// udmabuf turns memfd pages into a real dmabuf: a linear buffer any GPU
// driver imports. fill gets the page-aligned backing store, size bytes long.
func udmabuf(size int, fill func(data []byte)) (*os.File, error) {
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("udmabuf unavailable: %w", err)
	}
	defer dev.Close()
	size = (size + os.Getpagesize() - 1) / os.Getpagesize() * os.Getpagesize()
	mem, err := unix.MemfdCreate("testpattern-dmabuf", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	defer unix.Close(mem)
	if err := unix.Ftruncate(mem, int64(size)); err != nil {
		return nil, err
	}
	data, err := unix.Mmap(mem, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	fill(data)
	if err := unix.Munmap(data); err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		return nil, err
	}
	// struct udmabuf_create { u32 memfd; u32 flags; u64 offset; u64 size; }
	arg := struct {
		memfd, flags uint32
		offset, size uint64
	}{memfd: uint32(mem), flags: 1 /* CLOEXEC */, size: uint64(size)}
	const udmabufCreate = 0x40187542 // _IOW('u', 0x42, struct udmabuf_create)
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufCreate, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		return nil, fmt.Errorf("udmabuf create: %w", errno)
	}
	return os.NewFile(fd, "dmabuf"), nil
}
