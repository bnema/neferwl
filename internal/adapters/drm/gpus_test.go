package drm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestListGPUs(t *testing.T) {
	root := t.TempDir()
	sys, dev := filepath.Join(root, "sys"), filepath.Join(root, "dev")
	class := filepath.Join(sys, "class", "drm")
	mkdir := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		mkdir(filepath.Dir(path))
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		mkdir(filepath.Dir(path))
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ name, bus, address, driver, primary, render, boot string }{
		{"card0", "pci", "0000:00:02.0", "i915", "226:0", "129", "0"},
		{"card1", "pci", "0000:03:00.0", "amdgpu", "226:1", "128", "1"},
		{"card2", "usb", "1-2", "udl", "226:2", "", ""},
		{"card10", "", "platform-gpu", "", "", "", ""},
	} {
		device := filepath.Join(sys, "devices", c.address)
		card := filepath.Join(device, "drm", c.name)
		mkdir(card)
		link(filepath.Join("..", "..", "devices", c.address, "drm", c.name), filepath.Join(class, c.name))
		link(device, filepath.Join(card, "device"))
		if c.primary != "" {
			write(filepath.Join(card, "dev"), c.primary+"\n")
		}
		if c.bus != "" {
			link(filepath.Join(sys, "bus", c.bus), filepath.Join(device, "subsystem"))
		}
		if c.driver != "" {
			link(filepath.Join(sys, "drivers", c.driver), filepath.Join(device, "driver"))
		}
		if c.boot != "" {
			write(filepath.Join(device, "boot_vga"), c.boot+"\n")
		}
		if c.render != "" {
			write(filepath.Join(device, "drm", "renderD"+c.render, "dev"), "226:"+c.render+"\n")
		}
	}
	for name, status := range map[string]string{"card0-eDP-1": "connected", "card1-DP-2": "connected", "card1-LVDS-1": "disconnected", "card2-DSI-1": "disconnected"} {
		connector := filepath.Join(sys, "devices", "connectors", name)
		write(filepath.Join(connector, "status"), status+"\n")
		link(connector, filepath.Join(class, name))
	}
	got, err := ListGPUs(sys, dev)
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.GPUInfo{
		{Path: filepath.Join(dev, "dri/card0"), Primary: 57856, Render: 57985, PCI: "0000:00:02.0", Driver: "i915", Internal: true},
		{Path: filepath.Join(dev, "dri/card1"), Primary: 57857, Render: 57984, PCI: "0000:03:00.0", Driver: "amdgpu", BootVGA: true},
		{Path: filepath.Join(dev, "dri/card2"), Primary: 57858, Driver: "udl"},
		{Path: filepath.Join(dev, "dri/card10")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListGPUs = %+v; want %+v", got, want)
	}
}

func TestListGPUsClassError(t *testing.T) {
	if _, err := ListGPUs(t.TempDir(), "/dev"); err == nil {
		t.Fatal("missing class directory: expected error")
	}
}
