package vulkan

import (
	"image/color"
	"math"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Frames drawn into exported targets reach their dmabufs: another renderer
// importing a target (as KMS scans it out) sees the frame, and readback
// reads the target drawn last.
func TestExportTargets(t *testing.T) {
	r, err := New(64, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	bufs, err := r.ExportTargets(2, nil)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	defer func() {
		for _, b := range bufs {
			b.Planes[0].File.Close()
		}
	}()
	if len(bufs) != 2 || bufs[0].Width != 64 || bufs[0].Format != fourccXRGB || bufs[0].Planes[0].Stride < 64*4 {
		t.Fatalf("targets %+v", bufs)
	}
	colors := []string{"#ff0000", "#0000ff"}
	for i, c := range colors {
		r.UseTarget(i)
		if err := render(r, ports.Scene{Background: c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Pixels().At(3, 3); got != (color.RGBA{0, 0, 255, 255}) {
		t.Fatalf("readback of the last target %v", got)
	}
	// Import target 0 as a client buffer, like the display would.
	viewer, err := New(64, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	b := bufs[0]
	b.ID = 1
	importable := false
	for _, f := range viewer.DMABuf().Formats {
		importable = importable || f.Format == b.Format && f.Modifier == b.Modifier
	}
	if !importable {
		t.Skipf("modifier %#x not importable", b.Modifier)
	}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 32}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 64, Height: 32, Opaque: true, DMABuf: &b}}
	if err := render(viewer, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := viewer.Pixels().At(10, 10); got != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("target 0 as seen by the display %v", got)
	}
}

// The exported HDR image carries PQ while Pixels remains the composed SDR
// image. A test-only transfer-src readback checks the actual shader output.
func TestHDRExportTarget(t *testing.T) {
	r, err := New(32, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	r.SetHDR(203)
	bufs, err := r.ExportTargets(1, r.hdrMods)
	if err != nil {
		t.Skipf("no HDR-exportable target: %v", err)
	}
	defer bufs[0].Planes[0].File.Close()
	if bufs[0].Format != fourccXR30 {
		t.Fatalf("format: %#x", bufs[0].Format)
	}
	if err := render(r, ports.Scene{Background: "#ffffff"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.Pixels().RGBAAt(0, 0); got.R != 255 || got.G != 255 || got.B != 255 {
		t.Fatalf("SDR readback: %v", got)
	}
	// Temporarily read the 10-bit target back into the existing 4-byte/pixel
	// staging buffer, without changing the production Pixels path.
	if err := r.waitFrame(r.submitted); err != nil {
		t.Fatal(err)
	}
	target := r.targets[0]
	err = r.oneShot(func(cmd vk.CommandBuffer) {
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
	v := *(*uint32)(unsafe.Pointer(r.mapped))
	want, _, _ := hdrPixel(1, 1, 1, 203)
	for _, shift := range []uint{0, 10, 20} {
		got := float64(v>>shift&1023) / 1023
		if math.Abs(got-want) > 0.004 {
			t.Fatalf("channel %d: PQ %.5f want %.5f", shift, got, want)
		}
	}
}
