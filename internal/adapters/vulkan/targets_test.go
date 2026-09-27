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
	// Only this GPU test requests transfer-src on an HDR target.
	r.hdrReadback = true
	if r.physical == 0 {
		t.Skip("no exportable GPU")
	}
	r.hdrMods = r.probeModifiers(r.physical, vk.FormatA2r10g10b10UnormPack32)
	if len(r.hdrMods) == 0 {
		t.Skip("no HDR modifier supporting transfer-src")
	}
	bufs, err := r.ExportTargets(1, r.hdrMods)
	if err != nil {
		t.Skipf("no HDR-exportable target: %v", err)
	}
	defer bufs[0].Planes[0].File.Close()
	if bufs[0].Format != fourccXR30 {
		t.Fatalf("format: %#x", bufs[0].Format)
	}

	// A real second frame with an unchanged scene must retain the internal
	// image and use a partial-damage load from its actual prior layout.
	scene := ports.Scene{Seq: 1, Background: "#ffffff"}
	for _, tc := range []struct {
		bg  string
		rgb [3]float64
	}{
		{"#ffffff", [3]float64{1, 1, 1}},
		{"#ff0000", [3]float64{1, 0, 0}},
		{"#0000ff", [3]float64{0, 0, 1}},
		{"#808080", [3]float64{128.0 / 255, 128.0 / 255, 128.0 / 255}},
	} {
		scene.Seq++
		scene.Background = tc.bg
		if err := render(r, scene, nil); err != nil {
			t.Fatal(err)
		}
		checkHDRPixel(t, r, tc.rgb)
	}
	before := r.redrawn
	if err := render(r, scene, nil); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= r.width*r.height {
		t.Fatalf("unchanged HDR frame redrew %d pixels", r.redrawn-before)
	}
	checkHDRPixel(t, r, [3]float64{128.0 / 255, 128.0 / 255, 128.0 / 255})
	if r.hdrOwn.layout != vk.ImageLayoutTransferSrcOptimal {
		t.Fatalf("internal layout after HDR: %v", r.hdrOwn.layout)
	}
	if got := r.Pixels().RGBAAt(0, 0); got.R != 128 || got.G != 128 || got.B != 128 {
		t.Fatalf("SDR readback: %v", got)
	}
}

// Test-only target readback: production HDR images do not have transfer-src.
func checkHDRPixel(t *testing.T, r *Renderer, rgb [3]float64) {
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
	v := *(*uint32)(unsafe.Pointer(r.mapped))
	red, green, blue := hdrPixel(rgb[0], rgb[1], rgb[2], 203)
	for i, want := range []float64{blue, green, red} {
		got := float64(v>>uint(i*10)&1023) / 1023
		if math.Abs(got-want) > 0.005 {
			t.Fatalf("rgb %v channel %d: PQ %.5f want %.5f", rgb, i, got, want)
		}
	}
}
