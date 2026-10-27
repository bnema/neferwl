package core_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
)

func TestRenderDeviceID(t *testing.T) {
	for _, tt := range []struct {
		name, configured, environment, want string
	}{
		{name: "default"},
		{name: "configured", configured: "/dev/dri/card0", want: "/dev/dri/card0"},
		{name: "environment", environment: "03:00.0", want: "03:00.0"},
		{name: "override before resolution", configured: "/missing/card", environment: "03:00.0", want: "03:00.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := core.RenderDeviceID(tt.configured, tt.environment); got != tt.want {
				t.Fatalf("RenderDeviceID = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestSelectGPUs(t *testing.T) {
	candidates := []domain.GPUInfo{
		{Path: "/dev/dri/card0", PCI: "0000:00:02.0", Render: 226<<8 | 128, Internal: true, Driver: "i915"},
		{Path: "/dev/dri/card1", PCI: "0000:03:00.0", Render: 226<<8 | 129, BootVGA: true, Driver: "amdgpu"},
		{Path: "/dev/dri/card2", PCI: "0000:0a:00.0", Render: 226<<8 | 130, Driver: "nvidia-drm"},
	}
	tests := []struct {
		name   string
		cfg    core.GPUSelectConfig
		mode   string
		render int
		keep   []int
		reason string
		err    string
	}{
		{name: "default internal", render: 0, keep: []int{0, 1, 2}, reason: "internal"},
		{name: "boot VGA", mode: "no internal", render: 1, keep: []int{0, 1, 2}, reason: "boot_vga"},
		{name: "lowest index", mode: "no hints", render: 0, keep: []int{0, 1, 2}, reason: "lowest index"},
		{name: "card path", cfg: core.GPUSelectConfig{Render: "/dev/dri/card1"}, render: 1, keep: []int{0, 1, 2}, reason: "render.device"},
		{name: "render path", cfg: core.GPUSelectConfig{Render: "/dev/dri/renderD130"}, render: 2, keep: []int{0, 1, 2}, reason: "render.device"},
		{name: "PCI domain case insensitive", cfg: core.GPUSelectConfig{Render: "0000:0A:00.0"}, render: 2, keep: []int{0, 1, 2}, reason: "render.device"},
		{name: "PCI short case insensitive", cfg: core.GPUSelectConfig{Render: "0A:00.0"}, render: 2, keep: []int{0, 1, 2}, reason: "render.device"},
		{name: "ignore", cfg: core.GPUSelectConfig{Ignore: []string{"00:02.0"}}, render: 1, keep: []int{1, 2}, reason: "boot_vga"},
		{name: "ignored render", cfg: core.GPUSelectConfig{Ignore: []string{"03:00.0"}, Render: "/dev/dri/card1"}, err: `render device "/dev/dri/card1" is ignored`},
		{name: "allow subset sorted deduplicated", cfg: core.GPUSelectConfig{Devices: []string{"/dev/dri/renderD130", "03:00.0", "03:00.0"}}, render: 1, keep: []int{1, 2}, reason: "boot_vga"},
		{name: "ignore wins", cfg: core.GPUSelectConfig{Devices: []string{"03:00.0", "0a:00.0"}, Ignore: []string{"03:00.0"}}, render: 2, keep: []int{2}, reason: "lowest index"},
		{name: "non kept render", cfg: core.GPUSelectConfig{Devices: []string{"03:00.0"}, Render: "00:02.0"}, err: `render device "00:02.0" is ignored`},
		{name: "unknown devices", cfg: core.GPUSelectConfig{Devices: []string{"nope"}}, err: `unknown DRM device "nope"`},
		{name: "unknown ignore", cfg: core.GPUSelectConfig{Ignore: []string{"nope"}}, err: `unknown DRM device "nope"`},
		{name: "unknown render", cfg: core.GPUSelectConfig{Render: "nope"}, err: `unknown DRM device "nope"`},
		{name: "no render nodes", mode: "no render", err: "no render-capable DRM device"},
		{name: "all ignored", cfg: core.GPUSelectConfig{Ignore: []string{"00:02.0", "03:00.0", "0a:00.0"}}, err: "no render-capable DRM device"},
		{name: "empty", mode: "empty", err: "no render-capable DRM device"},
		{name: "skip internal without render", mode: "internal no render", render: 1, keep: []int{0, 1, 2}, reason: "boot_vga"},
		{name: "explicit without render", mode: "internal no render", cfg: core.GPUSelectConfig{Render: "00:02.0"}, err: "no render-capable DRM device"},
		{name: "absent render node cannot match", mode: "no render", cfg: core.GPUSelectConfig{Render: "/dev/dri/renderD0"}, err: `unknown DRM device "/dev/dri/renderD0"`},
		{name: "high minor bits", mode: "high minor", cfg: core.GPUSelectConfig{Render: "/dev/dri/renderD1048833"}, render: 2, keep: []int{0, 1, 2}, reason: "render.device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := slices.Clone(candidates)
			switch tt.mode {
			case "no internal":
				c[0].Internal = false
			case "no hints":
				c[0].Internal, c[1].BootVGA = false, false
			case "no render":
				for i := range c {
					c[i].Render = 0
				}
			case "internal no render":
				c[0].Render = 0
			case "empty":
				c = nil
			case "high minor":
				c[2].Render = 226<<8 | 1 | uint64(1048833&^0xff)<<12
			}
			before := slices.Clone(c)
			got, err := core.SelectGPUs(c, tt.cfg)
			if !reflect.DeepEqual(c, before) {
				t.Fatal("mutated candidates")
			}
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("error = %v, want %s", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Render != tt.render || !slices.Equal(got.Keep, tt.keep) {
				t.Fatalf("selection = %+v, want render %d keep %v", got, tt.render, tt.keep)
			}
			if len(got.Reasons) != len(c)+1 {
				t.Fatalf("reasons = %v", got.Reasons)
			}
			g := c[tt.render]
			want := fmt.Sprintf("render GPU card%d (%s): %s", tt.render, g.PCI, tt.reason)
			if got.Reasons[len(c)] != want {
				t.Fatalf("render reason = %q, want %q", got.Reasons[len(c)], want)
			}
			for i, line := range got.Reasons[:len(c)] {
				decision := "ignore"
				if slices.Contains(tt.keep, i) {
					decision = "keep"
				}
				if !strings.HasPrefix(line, decision+" GPU ") {
					t.Fatalf("decision reason = %q", line)
				}
			}
		})
	}
}

func TestSelectGPUsPCIDomains(t *testing.T) {
	c := []domain.GPUInfo{{PCI: "0001:03:00.0", Render: 1}, {PCI: "0002:03:00.0", Render: 2}, {PCI: "0000:04:00.0", Render: 3}}
	for _, tc := range []struct {
		name   string
		cfg    core.GPUSelectConfig
		keep   []int
		render int
	}{
		{"render first", core.GPUSelectConfig{Render: "03:00.0"}, []int{0, 1, 2}, 0},
		{"devices both", core.GPUSelectConfig{Devices: []string{"03:00.0"}}, []int{0, 1}, 0},
		{"ignore both", core.GPUSelectConfig{Ignore: []string{"03:00.0"}}, []int{2}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := core.SelectGPUs(c, tc.cfg)
			if err != nil || !slices.Equal(sel.Keep, tc.keep) || sel.Render != tc.render {
				t.Fatalf("%+v %v", sel, err)
			}
		})
	}
}
