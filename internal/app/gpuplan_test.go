package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPlanCards(t *testing.T) {
	c := []ports.GPUInfo{{Path: "card0"}, {Path: "card1"}, {Path: "card2"}}
	for _, tc := range []struct {
		name string
		sel  ports.GPUSelection
		want []cardPlan
	}{
		{"ignored absent", ports.GPUSelection{Keep: []int{0, 2}, Render: 2}, []cardPlan{{c[0], false}, {c[2], true}}},
		{"selected only and order", ports.GPUSelection{Keep: []int{0, 1, 2}, Render: 1}, []cardPlan{{c[0], false}, {c[1], true}, {c[2], false}}},
		{"empty", ports.GPUSelection{}, []cardPlan{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := planCards(c, tc.sel); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveIDs(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "card")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := resolveIDs([]string{link, "0000:03:00.0"})
	if err != nil || !reflect.DeepEqual(got, []string{target, "0000:03:00.0"}) {
		t.Fatalf("%v %v", got, err)
	}
	missing := filepath.Join(dir, "missing")
	if _, err := resolveIDs([]string{missing}); err == nil || !strings.Contains(err.Error(), "DRM device ") {
		t.Fatal(err)
	}
}

func TestSelectConfig(t *testing.T) {
	cfg := ports.Config{}
	cfg.DRM.Devices = []string{"0000:03:00.0"}
	cfg.DRM.Ignore = []string{"04:00.0"}
	cfg.Render.Device = "03:00.0"
	for _, env := range []string{"", "05:00.0"} {
		got, err := gpuSelectConfig(cfg, env)
		if err != nil {
			t.Fatal(err)
		}
		want := cfg.Render.Device
		if env != "" {
			want = env
		}
		if got.Render != want || !reflect.DeepEqual(got.Devices, cfg.DRM.Devices) || !reflect.DeepEqual(got.Ignore, cfg.DRM.Ignore) {
			t.Fatalf("%+v", got)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, field := range []string{"devices", "ignore", "render", "env"} {
		t.Run(field, func(t *testing.T) {
			c := ports.Config{}
			env := ""
			switch field {
			case "devices":
				c.DRM.Devices = []string{missing}
			case "ignore":
				c.DRM.Ignore = []string{missing}
			case "render":
				c.Render.Device = missing
			case "env":
				env = missing
			}
			if _, err := gpuSelectConfig(c, env); err == nil {
				t.Fatal("missing path accepted")
			}
		})
	}
	cfg.Render.Device = missing
	if got, err := gpuSelectConfig(cfg, "05:00.0"); err != nil || got.Render != "05:00.0" {
		t.Fatalf("override did not win: %+v %v", got, err)
	}
}

func TestMultiGPUMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want drm.MultiGPU
	}{{"", drm.MultiGPUAuto}, {"auto", drm.MultiGPUAuto}, {"direct", drm.MultiGPUDirect}, {"off", drm.MultiGPUOff}} {
		if got := multiGPUMode(tc.in); got != tc.want {
			t.Fatalf("%q: %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestGPUSelectionErrorBeforePlan(t *testing.T) {
	cfg := ports.Config{}
	cfg.DRM.Ignore = []string{"unknown-device"}
	plans, _, err := selectGPUPlans([]ports.GPUInfo{{Path: "card0", Render: 1}}, cfg, "")
	if err == nil || !strings.Contains(err.Error(), `unknown DRM device "unknown-device"`) || plans != nil {
		t.Fatalf("%v %v", plans, err)
	}
}
