package drm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/bnema/neferwl/internal/adapters/fdwait"
	"github.com/bnema/purego"
	"golang.org/x/sys/unix"
)

// Hotplug reports connector changes through a udev netlink monitor on the
// drm subsystem (the kernel sends HOTPLUG=1 on the card). libudev is loaded
// with purego.

// Hotplug actions reported in HotplugEvent.Action.
const (
	HotplugAdd    = "add"
	HotplugRemove = "remove"
	HotplugChange = "change"
)

// HotplugEvent is a DRM uevent the compositor reacts to. Card is the sysname
// ("card1") of a GPU that was added or removed; it is empty for a connector
// change (HOTPLUG=1), which only asks for a rescan.
type HotplugEvent struct {
	Card   string
	Action string
}

// hotplugFromProps classifies an event from its udev properties: ok is false
// for events the compositor ignores (other subsystems, render nodes, connector
// devices appearing or disappearing, card changes that are not connector
// hotplugs). The libudev watcher feeds it property lookups.
func hotplugFromProps(get func(key string) string) (HotplugEvent, bool) {
	if get("SUBSYSTEM") != "drm" {
		return HotplugEvent{}, false
	}
	action := get("ACTION")
	switch action {
	case HotplugAdd, HotplugRemove, HotplugChange:
	default:
		return HotplugEvent{}, false
	}
	if action == HotplugChange {
		if get("HOTPLUG") == "1" {
			return HotplugEvent{Action: action}, true
		}
		return HotplugEvent{}, false
	}
	name := get("DEVNAME")
	if name == "" {
		name = get("DEVPATH")
	}
	name = path.Base(name)
	if !isCardName(name) {
		return HotplugEvent{}, false
	}
	return HotplugEvent{Card: name, Action: action}, true
}

// isCardName reports whether name is "card" followed by digits only: render
// nodes (renderD128) and connectors (card1-DP-2) are not cards.
func isCardName(name string) bool {
	digits, found := strings.CutPrefix(name, "card")
	if !found || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

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

// DeliverHotplug hands ev to changed. A connector change (Card == "") only
// asks for a rescan, so it is dropped when one is already queued. A card
// add or remove is never dropped: the send blocks until the consumer takes
// it or ctx ends. It reports whether ev was queued.
func DeliverHotplug(ctx context.Context, changed chan<- HotplugEvent, ev HotplugEvent) bool {
	if ev.Card == "" {
		select {
		case changed <- ev:
			return true
		default:
			return false
		}
	}
	select {
	case changed <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// WatchHotplug sends every DRM card add or remove and every connector change
// on changed until ctx ends (see DeliverHotplug for the send policy).
func WatchHotplug(ctx context.Context, changed chan<- HotplugEvent) error {
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
	waiter, err := fdwait.New()
	if err != nil {
		return fmt.Errorf("udev waiter: %w", err)
	}
	defer waiter.Close()
	waiter.StopOn(ctx)
	if err := waiter.Pin(fd); err != nil {
		return fmt.Errorf("udev waiter: %w", err)
	}
	fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
	for ctx.Err() == nil {
		n, err := waiter.Poll(fds, -1)
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
		ev, ok := hotplugFromProps(func(key string) string { return devicePropertyVal(dev, key) })
		deviceUnref(dev)
		if ok {
			DeliverHotplug(ctx, changed, ev)
		}
	}
	return nil
}
