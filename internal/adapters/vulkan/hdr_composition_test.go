package vulkan

import (
	"image"
	"image/color"
	"math"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

func hdrTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	t.Cleanup(r.Close)
	r.SetHDR(203)
	r.hdrReadback = true
	if r.physical == 0 {
		t.Skip("no exportable GPU")
	}
	r.hdrMods = r.probeModifiers(r.physical, vk.FormatA2r10g10b10UnormPack32)
	if len(r.hdrMods) == 0 {
		t.Skip("no HDR transfer-src modifier")
	}
	bufs, err := r.ExportTargets(1, r.hdrMods)
	if err != nil {
		t.Skipf("no HDR target: %v", err)
	}
	t.Cleanup(func() { bufs[0].Planes[0].File.Close() })
	return r
}

// Readback of a test-only PQ target (production targets are not transfer-src).
func hdrTargetAt(t *testing.T, r *Renderer, x, y int) [3]float64 {
	t.Helper()
	if err := r.waitFrame(r.submitted); err != nil {
		t.Fatal(err)
	}
	target := r.targets[0]
	err := r.oneShot(func(cmd vk.CommandBuffer) {
		d := r.dd
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: vk.ImageLayoutGeneral, NewLayout: vk.ImageLayoutTransferSrcOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.family, Image: target.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		region := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
		d.CmdCopyImageToBuffer(cmd, target.image, vk.ImageLayoutTransferSrcOptimal, r.buffer, 1, &region)
		b.SrcAccessMask, b.DstAccessMask = vk.AccessTransferReadBit, 0
		b.OldLayout, b.NewLayout = vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
	})
	if err != nil {
		t.Fatal(err)
	}
	words := unsafe.Slice((*uint32)(r.mapped), r.width*r.height)
	pixel := words[y*r.width+x]
	return [3]float64{float64(pixel>>20&1023) / 1023, float64(pixel>>10&1023) / 1023, float64(pixel&1023) / 1023}
}

func TestHDRWindowedComposition(t *testing.T) {
	r := hdrTestRenderer(t)
	format := ports.DMABufFormat{Format: fourcc('X', 'R', '3', '0'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, format) {
		t.Skip("linear XR30 import unavailable")
	}
	// 1000-nit neutral, red and blue BT.2020 client pixels.
	nits := [3][3]float64{{1000, 1000, 1000}, {1000, 0, 0}, {0, 0, 1000}}
	f := udmabuf(t, 64, 16, func(x, _ int) [4]byte {
		v := nits[min(x/20, 2)]
		b, g, red := uint32(math.Round(pqEncode(v[2])*1023)), uint32(math.Round(pqEncode(v[1])*1023)), uint32(math.Round(pqEncode(v[0])*1023))
		p := b | g<<10 | red<<20
		return [4]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)}
	})
	buf := &ports.DMABuf{ID: 2020, Width: 64, Height: 16, Format: format.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	c := ports.SurfaceContent{ID: 1, Seq: 1, Width: 64, Height: 16, LogicalW: 64, LogicalH: 16, Opaque: true, DMABuf: buf, Color: ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}}
	scene := ports.Scene{Seq: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: c}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	for i, v := range nits {
		got := hdrTargetAt(t, r, i*20+5, 8)
		want := [3]float64{
			pqEncode(v[0]),
			pqEncode(v[1]),
			pqEncode(v[2]),
		}
		for ch := range got {
			if math.Abs(got[ch]-want[ch]) > .012 {
				t.Errorf("sample %d channel %d got %.4f want %.4f", i, ch, got[ch], want[ch])
			}
		}
	}
	neutral := hdrTargetAt(t, r, 5, 8)
	if math.Abs(neutral[0]-pqEncode(1000)) > .008 {
		t.Fatalf("1000 nits clipped: %v", neutral)
	}
	if px := r.Pixels().RGBAAt(5, 8); !near(px, color.RGBA{255, 255, 255, 255}, 2) {
		t.Fatalf("clipped capture pixel %v", px)
	}
	dst := make([]byte, 8)
	if err := r.Capture(image.Rect(5, 8, 7, 9), dst, 8); err != nil {
		t.Fatal(err)
	}
	if dst[0] != 255 || dst[1] != 255 || dst[2] != 255 || dst[3] != 255 {
		t.Fatalf("capture %v", dst)
	}
	before := r.redrawn
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= r.width*r.height {
		t.Fatal("unchanged HDR frame redrew full image")
	}
	if got := hdrTargetAt(t, r, 5, 8); math.Abs(got[0]-pqEncode(1000)) > .008 {
		t.Fatalf("partial redraw lost highlights: %v", got)
	}
}

func TestHDRLinearBlend(t *testing.T) {
	r := hdrTestRenderer(t)
	// Two translucent SDR overlays over black: half white then half black.
	white := shmContent(t, 64, 16, 64*4, fill(64, 16, [4]byte{128, 128, 128, 128}))
	black := shmContent(t, 64, 16, 64*4, fill(64, 16, [4]byte{0, 0, 0, 128}))
	scene := ports.Scene{Background: "#000000", Layers: []ports.SceneLayer{{ID: 1, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 64, H: 16}}, {ID: 2, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: *white, 2: *black}); err != nil {
		t.Fatal(err)
	}
	got := hdrTargetAt(t, r, 5, 5)
	want := pqEncode(.25 * 203)
	for i, v := range got {
		if math.Abs(v-want) > .012 {
			t.Errorf("channel %d: got %.4f want %.4f", i, v, want)
		}
	}
}
