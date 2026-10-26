package vulkan

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCopierImportsLinear(t *testing.T) {
	g := newTestGPU(t)
	renderer, err := g.NewRenderer(32, 16, ports.RendererOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer renderer.Close()
	bufs, err := renderer.ExportTargets(2, []uint64{0}, true)
	if err != nil {
		t.Fatalf("LINEAR export: %v", err)
	}
	defer func() {
		for _, b := range bufs {
			for _, p := range b.Planes {
				p.File.Close()
			}
		}
	}()
	copier, err := g.NewCopier(32, 16)
	if err != nil {
		t.Fatal(err)
	}
	c := copier.(*Copier)
	q, users := c.q, c.q.users
	defer c.Close()
	if err := c.ImportSources(bufs); err != nil {
		t.Fatal(err)
	}
	if err := c.ImportTargets(bufs); err != nil {
		t.Fatal(err)
	}
	mods := c.SourceModifiers(g.DMABuf().Formats)
	linear := ports.DMABufFormat{Format: bufs[0].Format, Modifier: 0}
	if len(mods) == 0 || !slices.Contains(mods, linear) {
		t.Fatalf("LINEAR source absent: %v", mods)
	}
	c.Close()
	if q.users != users-1 || c.q != nil || c.pool != 0 || len(c.sources) != 0 || len(c.targets) != 0 {
		t.Fatal("copier resources retained or queue reference not released exactly once")
	}
	c.Close()
	if q.users != users-1 {
		t.Fatal("second Close changed queue users")
	}
	if _, err := g.NewCopier(0, 16); err == nil {
		t.Fatal("invalid dimensions accepted")
	}
}
