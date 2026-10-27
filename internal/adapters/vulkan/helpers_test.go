package vulkan

import (
	"os"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

func newTestGPU(t testing.TB) *GPU {
	t.Helper()
	var selected domain.GPUInfo
	if path := os.Getenv("NEFERWL_RENDER_DEVICE"); strings.HasPrefix(path, "/dev/dri/renderD") {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			t.Skipf("render node unavailable: %v", err)
		}
		selected.Render = stat.Rdev
	}
	inst, err := NewInstance(zerowrap.Logger{})
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	t.Cleanup(inst.Close)
	g, err := inst.OpenGPU(selected)
	if err != nil {
		t.Skipf("Vulkan GPU unavailable: %v", err)
	}
	t.Cleanup(g.Close)
	return g
}

func newTestRenderer(t testing.TB, w, h int) *Renderer {
	t.Helper()
	g := newTestGPU(t)
	renderer, err := g.NewRenderer(w, h, domain.RendererOptions{})
	if err != nil {
		t.Skipf("Vulkan renderer unavailable: %v", err)
	}
	r := renderer.(*Renderer)
	t.Cleanup(r.Close)
	return r
}
