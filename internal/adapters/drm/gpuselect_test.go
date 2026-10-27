package drm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

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
	got, err := ResolveDeviceIDs([]string{link, "0000:03:00.0"})
	if err != nil || !reflect.DeepEqual(got, []string{target, "0000:03:00.0"}) {
		t.Fatalf("%v %v", got, err)
	}
	missing := filepath.Join(dir, "missing")
	if _, err := ResolveDeviceIDs([]string{missing}); err == nil || !strings.Contains(err.Error(), "DRM device ") {
		t.Fatal(err)
	}
}

func TestSelectConfig(t *testing.T) {
	cfg := domain.Config{}
	cfg.DRM.Devices = []string{"0000:03:00.0"}
	cfg.DRM.Ignore = []string{"04:00.0"}
	cfg.Render.Device = "03:00.0"
	for _, env := range []string{"", "05:00.0"} {
		got, err := SelectConfig(cfg, env)
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
			c := domain.Config{}
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
			if _, err := SelectConfig(c, env); err == nil {
				t.Fatal("missing path accepted")
			}
		})
	}
	cfg.Render.Device = missing
	if got, err := SelectConfig(cfg, "05:00.0"); err != nil || got.Render != "05:00.0" {
		t.Fatalf("override did not win: %+v %v", got, err)
	}
}
