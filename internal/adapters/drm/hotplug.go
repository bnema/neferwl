package drm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bnema/purego"
	"golang.org/x/sys/unix"
)

// Hotplug reports connector changes through a udev netlink monitor on the
// drm subsystem (the kernel sends HOTPLUG=1 on the card). libudev is loaded
// with purego.

var (
	udevOnce sync.Once
	udevErr  error

	udevNew           func() uintptr
	udevUnref         func(uintptr) uintptr
	monitorNew        func(udev uintptr, name string) uintptr
	monitorUnref      func(uintptr) uintptr
	monitorFilter     func(mon uintptr, subsystem string, devtype uintptr) int32
	monitorEnable     func(uintptr) int32
	monitorFD         func(uintptr) int32
	monitorReceive    func(uintptr) uintptr
	deviceUnref       func(uintptr) uintptr
	devicePropertyVal func(dev uintptr, key string) string
)

func loadUdev() error {
	udevOnce.Do(func() {
		lib, err := purego.Dlopen("libudev.so.1", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			udevErr = fmt.Errorf("open libudev.so.1: %w", err)
			return
		}
		defer func() {
			if r := recover(); r != nil {
				udevErr = fmt.Errorf("libudev: %v", r)
			}
		}()
		reg := func(fn any, name string) { purego.RegisterLibFunc(fn, lib, "udev_"+name) }
		reg(&udevNew, "new")
		reg(&udevUnref, "unref")
		reg(&monitorNew, "monitor_new_from_netlink")
		reg(&monitorUnref, "monitor_unref")
		reg(&monitorFilter, "monitor_filter_add_match_subsystem_devtype")
		reg(&monitorEnable, "monitor_enable_receiving")
		reg(&monitorFD, "monitor_get_fd")
		reg(&monitorReceive, "monitor_receive_device")
		reg(&deviceUnref, "device_unref")
		reg(&devicePropertyVal, "device_get_property_value")
	})
	return udevErr
}

// WatchHotplug sends on changed after every DRM hotplug event until ctx
// ends. Events that arrive while a send is pending are merged.
func WatchHotplug(ctx context.Context, changed chan<- struct{}) error {
	if err := loadUdev(); err != nil {
		return err
	}
	udev := udevNew()
	if udev == 0 {
		return errors.New("udev_new failed")
	}
	defer udevUnref(udev)
	mon := monitorNew(udev, "udev")
	if mon == 0 {
		return errors.New("udev_monitor_new_from_netlink failed")
	}
	defer monitorUnref(mon)
	if monitorFilter(mon, "drm", 0) < 0 || monitorEnable(mon) < 0 {
		return errors.New("udev monitor setup failed")
	}
	fd := monitorFD(mon)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("udev poll: %w", err)
		}
		if n <= 0 {
			continue
		}
		dev := monitorReceive(mon)
		if dev == 0 {
			continue
		}
		hot := devicePropertyVal(dev, "HOTPLUG") == "1"
		deviceUnref(dev)
		if !hot {
			continue
		}
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	return nil
}
