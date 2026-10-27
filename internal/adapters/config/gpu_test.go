package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestGPUDefaults(t *testing.T) {
	for _, c := range []domain.Config{Defaults(), func() domain.Config {
		c, w := parseString(t, "")
		if len(w) != 0 {
			t.Fatal(w)
		}
		return c
	}()} {
		if len(c.DRM.Devices) != 0 || len(c.DRM.Ignore) != 0 || c.Render.Device != "" || c.Render.MultiGPU != "auto" {
			t.Fatalf("GPU defaults: %+v %+v", c.DRM, c.Render)
		}
	}
}

func TestDRMLists(t *testing.T) {
	for _, key := range []string{"drm.devices", "drm.ignore"} {
		t.Run(key, func(t *testing.T) {
			for _, tc := range []struct {
				value   string
				want    []string
				warning string
			}{
				{" /dev/dri/card0 , /dev/dri/by-path/pci-0000:03:00.0-card, 03:00.0 ", []string{"/dev/dri/card0", "/dev/dri/by-path/pci-0000:03:00.0-card", "03:00.0"}, ""},
				{"", nil, ""}, {"   ", nil, ""},
				{",card0", nil, "DRM device must not be empty"},
				{"card0,", nil, "DRM device must not be empty"},
				{"card0, ,card1", nil, "DRM device must not be empty"},
				{"card0, card0", nil, `duplicate DRM device "card0"`},
			} {
				t.Run(tc.value, func(t *testing.T) {
					c, w := parseString(t, key+" = "+tc.value+"\n")
					got := c.DRM.Devices
					if key == "drm.ignore" {
						got = c.DRM.Ignore
					}
					if !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("list = %q, want %q", got, tc.want)
					}
					if tc.warning == "" {
						if len(w) != 0 {
							t.Fatal(w)
						}
					} else if len(w) != 1 || w[0].Msg != key+": "+tc.warning {
						t.Fatalf("warnings: %v", w)
					}
				})
			}
			// Applying a reset or invalid replacement must not leave partial lists.
			for _, value := range []string{"", "  ", "new,", "new,new"} {
				c := Defaults()
				if err := set(&c, key, "old"); err != nil {
					t.Fatal(err)
				}
				err := set(&c, key, value)
				got := c.DRM.Devices
				if key == "drm.ignore" {
					got = c.DRM.Ignore
				}
				if strings.TrimSpace(value) == "" {
					if err != nil || len(got) != 0 {
						t.Fatalf("reset: %v %q", err, got)
					}
				} else if err == nil || !reflect.DeepEqual(got, []string{"old"}) {
					t.Fatalf("invalid replacement: %v %q", err, got)
				}
			}
		})
	}
}

func TestRenderDevice(t *testing.T) {
	// Identifiers are opaque here; discovery and identifier validation happen at startup.
	for _, value := range []string{"", " /dev/dri/card1 ", " /dev/dri/by-path/pci-0000:03:00.0-card ", " 03:00.0 ", " unknown-device "} {
		c, w := parseString(t, "render.device = "+value+"\n")
		if len(w) != 0 || c.Render.Device != strings.TrimSpace(value) {
			t.Fatalf("device: %q %v", c.Render.Device, w)
		}
	}
	c := Defaults()
	c.Render.Device = "old"
	if err := set(&c, "render.device", "  "); err != nil || c.Render.Device != "" {
		t.Fatalf("reset: %q %v", c.Render.Device, err)
	}
}

func TestRenderMultiGPU(t *testing.T) {
	for _, value := range []string{"auto", "direct", "off", "", "AUTO", "on", "gpu-copy"} {
		t.Run(value, func(t *testing.T) {
			c, w := parseString(t, "render.multi-gpu = "+value+"\n")
			switch value {
			case "auto", "direct", "off":
				if len(w) != 0 || c.Render.MultiGPU != value {
					t.Fatal(c.Render, w)
				}
			default:
				if c.Render.MultiGPU != "auto" || len(w) != 1 || w[0].Msg != "render.multi-gpu: must be auto, direct or off" {
					t.Fatal(c.Render, w)
				}
			}
		})
	}
}
