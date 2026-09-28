package ddc

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// I2C_RDWR from linux/i2c-dev.h and I2C_M_RD from linux/i2c.h.
const (
	ioctlI2CRdwr = 0x0707
	i2cRead      = 0x0001
)

type i2cMsg struct {
	addr  uint16
	flags uint16
	len   uint16
	_     uint16
	buf   *byte
}

type i2cRdwr struct {
	msgs  *i2cMsg
	nmsgs uint32
}

// i2cDev is a /dev/i2c-N character device (i2c-dev module).
type i2cDev struct{ fd int }

func openI2C(path string) (*i2cDev, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &i2cDev{fd}, nil
}

func (d *i2cDev) write(addr uint16, b []byte) error { return d.xfer(addr, 0, b) }

func (d *i2cDev) read(addr uint16, b []byte) error { return d.xfer(addr, i2cRead, b) }

func (d *i2cDev) close() error { return unix.Close(d.fd) }

func (d *i2cDev) xfer(addr, flags uint16, b []byte) error {
	msg := i2cMsg{addr: addr, flags: flags, len: uint16(len(b)), buf: &b[0]}
	data := i2cRdwr{msgs: &msg, nmsgs: 1}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d.fd), ioctlI2CRdwr, uintptr(unsafe.Pointer(&data)))
	runtime.KeepAlive(b)
	if errno != 0 {
		return errno
	}
	return nil
}
