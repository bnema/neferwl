package vulkan

import (
	"context"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

func TestCopierImportsLinear(t *testing.T) {
	g := newTestGPU(t)
	if !g.syncFD {
		t.Skip("GPU lacks sync_fd export")
	}
	renderer, err := g.NewRenderer(32, 16, ports.RendererOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer renderer.Close()
	if !slices.Contains(renderer.TargetModifiers([]uint64{0}, true), uint64(0)) {
		t.Skipf("no LINEAR export on %s", g.deviceInfo.Name)
	}
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
	requireCopyImports(t, c, bufs)
	if err := c.ImportSources(bufs); err != nil {
		t.Fatal(err)
	}
	if err := c.ImportTargets(bufs); err != nil {
		t.Fatal(err)
	}
	mods := c.SourceModifiers(g.DMABuf().Formats)
	linear := domain.DMABufFormat{Format: bufs[0].Format, Modifier: 0}
	if len(mods) == 0 || !slices.Contains(mods, linear) {
		t.Skipf("LINEAR source modifier absent: %v", mods)
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

//testpolicy:realtime waits on real GPU sync_file completion with a bounded context
func TestCopierRoundTrip(t *testing.T) {
	g := newTestGPU(t)
	if !g.syncFD {
		t.Skip("GPU lacks sync_fd export")
	}
	var rs [2]*Renderer
	var buffers [2][]ports.DMABuf
	for i := range rs {
		renderer, err := g.NewRenderer(32, 16, ports.RendererOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rs[i] = renderer.(*Renderer)
		defer rs[i].Close()
		if !slices.Contains(rs[i].TargetModifiers([]uint64{0}, true), uint64(0)) {
			t.Skipf("no LINEAR export on %s", g.deviceInfo.Name)
		}
		bufs, err := rs[i].ExportTargets(2, []uint64{0}, true)
		if err != nil {
			t.Fatal(err)
		}
		buffers[i] = bufs
		defer func() {
			for _, b := range bufs {
				for _, p := range b.Planes {
					p.File.Close()
				}
			}
		}()
	}
	copier, err := g.NewCopier(32, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer copier.Close()
	requireCopyImports(t, copier.(*Copier), buffers[0])
	requireCopyImports(t, copier.(*Copier), buffers[1])
	if err := copier.ImportSources(buffers[0]); err != nil {
		t.Fatal(err)
	}
	if err := copier.ImportTargets(buffers[1]); err != nil {
		t.Fatal(err)
	}
	rs[0].UseTarget(0)
	acquire, err := rs[0].Render(ports.Scene{Background: "#123456"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acquire != nil {
		defer acquire.Close()
	}
	// Initialize B's target/readback bookkeeping without using it after copy.
	rs[1].UseTarget(0)
	if err := render(rs[1], ports.Scene{Background: "#000000"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := rs[1].waitOwnIdle(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := range 5 { // Exercises fence reuse beyond the three-command ring.
		wait := acquire
		if i > 0 {
			wait = nil
		}
		done, err := copier.Copy(0, 0, wait)
		if err != nil {
			t.Fatal(err)
		}
		if done != nil {
			err = syncfile.Wait(ctx, done)
			done.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if acquire != nil {
		if _, err := acquire.Stat(); err != nil {
			t.Fatal("caller acquire file consumed")
		}
	}
	pixels := rs[1].Pixels()
	if pixels == nil || pixels.RGBAAt(8, 8) != (color.RGBA{0x12, 0x34, 0x56, 255}) {
		t.Fatalf("copy pixel mismatch: %v", pixels)
	}
	copier.Close()
	if _, err := copier.Copy(0, 0, nil); err == nil {
		t.Fatal("Copy after Close accepted")
	}
}

// Check advertised capabilities first; import failures remain fatal once supported.
func requireCopyImports(t *testing.T, c *Copier, bufs []ports.DMABuf) {
	t.Helper()
	for _, b := range bufs {
		format, _, _ := vkFormat(b.Format)
		for _, usage := range []vk.ImageUsageFlags{vk.ImageUsageTransferSrcBit, vk.ImageUsageTransferDstBit} {
			need := vk.FormatFeatureFlags(vk.FormatFeatureBlitSrcBit)
			if usage == vk.ImageUsageTransferDstBit {
				need = vk.FormatFeatureBlitDstBit
			}
			if _, ok := c.supportsBlit(format, b.Modifier, need); !ok || !c.gpu.copyImportable(format, b.Modifier, usage) {
				t.Skipf("ImportSources/ImportTargets unsupported for format %#x modifier %#x usage %#x", b.Format, b.Modifier, usage)
			}
		}
	}
}
