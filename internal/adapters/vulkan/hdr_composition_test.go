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

// pqTargetWords reads a test-only PQ target back (production targets are
// not transfer-src), staging through a capture slot's buffer.
func pqTargetWords(t *testing.T, r *Renderer) []uint32 {
	t.Helper()
	if err := r.waitFrame(r.submitted); err != nil {
		t.Fatal(err)
	}
	if err := r.createCaptureSlots(); err != nil {
		t.Fatal(err)
	}
	slot := r.captures[0]
	if slot.leased {
		t.Fatal("capture slot 0 leased")
	}
	target := r.targets[0]
	err := r.oneShot(func(cmd vk.CommandBuffer) {
		d := r.dd
		b := vk.ImageMemoryBarrier{SType: vk.StructureTypeImageMemoryBarrier, DstAccessMask: vk.AccessTransferReadBit, OldLayout: vk.ImageLayoutGeneral, NewLayout: vk.ImageLayoutTransferSrcOptimal, SrcQueueFamilyIndex: vk.QueueFamilyForeignEXT, DstQueueFamilyIndex: r.family, Image: target.image, SubresourceRange: colorRange}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit, 0, 0, nil, 0, nil, 1, &b)
		region := vk.BufferImageCopy{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectColorBit, LayerCount: 1}, ImageExtent: vk.Extent3D{Width: uint32(r.width), Height: uint32(r.height), Depth: 1}}
		d.CmdCopyImageToBuffer(cmd, target.image, vk.ImageLayoutTransferSrcOptimal, slot.buffer, 1, &region)
		hb := vk.BufferMemoryBarrier{SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: vk.AccessTransferWriteBit, DstAccessMask: vk.AccessHostReadBit, SrcQueueFamilyIndex: queueFamilyIgnored, DstQueueFamilyIndex: queueFamilyIgnored, Buffer: slot.buffer, Size: wholeSize}
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageHostBit, 0, 0, nil, 1, &hb, 0, nil)
		b.SrcAccessMask, b.DstAccessMask = vk.AccessTransferReadBit, 0
		b.OldLayout, b.NewLayout = vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = r.family, vk.QueueFamilyForeignEXT
		d.CmdPipelineBarrier(cmd, vk.PipelineStageTransferBit, vk.PipelineStageBottomOfPipeBit, 0, 0, nil, 0, nil, 1, &b)
	})
	if err != nil {
		t.Fatal(err)
	}
	return unsafe.Slice((*uint32)(slot.mapped), r.width*r.height)
}

func hdrTargetAt(t *testing.T, r *Renderer, x, y int) [3]float64 {
	t.Helper()
	pixel := pqTargetWords(t, r)[y*r.width+x]
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
	// SDR capture tone-maps: the 1000-nit white lands on the shoulder
	// (bright, not saturated) and the primaries keep their hue.
	want := captureSDR([3]float64{1000.0 / 203, 1000.0 / 203, 1000.0 / 203})
	if px := readPixels(t, r).RGBAAt(5, 8); !near(px, color.RGBA{want[0], want[1], want[2], 255}, 2) {
		t.Fatalf("tone-mapped capture pixel %v, want %v", px, want)
	}
	t.Run("async capture", func(t *testing.T) {
		cf := captureWait(t, r)
		defer r.EndCapture(cf)
		dst := make([]byte, 8)
		if err := cf.Read(image.Rect(5, 8, 7, 9), dst, 8); err != nil {
			t.Fatal(err)
		}
		if !near(color.RGBA{dst[2], dst[1], dst[0], 255}, color.RGBA{want[0], want[1], want[2], 255}, 2) || dst[3] != 255 {
			t.Fatalf("capture %v, want %v", dst, want)
		}
	})
	// The BT.2020 red (negative BT.709 green and blue) matches the
	// reference gamut reduction and shoulder, not a per-channel clip.
	red := readPixels(t, r).RGBAAt(25, 8)
	lin := [3]float64{1.660491, -0.124550, -0.018151}
	for i := range lin {
		lin[i] *= 1000.0 / 203
	}
	wantRed := captureSDR(lin)
	if !near(red, color.RGBA{wantRed[0], wantRed[1], wantRed[2], 255}, 3) || red.G != 0 {
		t.Fatalf("bright BT.2020 red captured as %v, want %v", red, wantRed)
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

func TestHDRSolidColorsMatchPartialRedraw(t *testing.T) {
	r := hdrTestRenderer(t)
	const background = "#804020"
	const border = "#4080c0"
	scene := ports.Scene{Seq: 1, Background: background, Border: ports.Border{Active: border}, Separators: []ports.Separator{{Rect: ports.Rect{X: 16, Y: 2, W: 2, H: 12}, Active: true}}, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 20, Y: 2, W: 28, H: 12}}}}
	// The window body is a solid fill; the separator is another solid draw.
	content := solidContent(t, 28, 12, color.RGBA{128, 64, 32, 255})
	content.ID, content.Seq = 1, 1
	check := func(label string) {
		t.Helper()
		for _, point := range []struct {
			x, y int
			hex  string
		}{{2, 2, background}, {16, 4, border}, {24, 8, background}} {
			got := hdrTargetAt(t, r, point.x, point.y)
			col := parseColor(point.hex)
			var linear [3]float64
			for ch, b := range col {
				v := float64(b) / 255
				if v > .04045 {
					v = math.Pow((v+.055)/1.055, 2.4)
				} else {
					v /= 12.92
				}
				linear[ch] = v
			}
			matrix := [3][3]float64{{.627404, .329283, .043313}, {.069097, .919540, .011362}, {.016391, .088013, .895595}}
			for ch := range got {
				want := pqEncode((matrix[ch][0]*linear[0] + matrix[ch][1]*linear[1] + matrix[ch][2]*linear[2]) * 203)
				if math.Abs(got[ch]-want) > .012 {
					t.Errorf("%s (%d,%d) channel %d got %.4f want %.4f", label, point.x, point.y, ch, got[ch], want)
				}
			}
		}
	}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: content}); err != nil {
		t.Fatal(err)
	}
	check("full")
	before := r.redrawn
	// A changed window sequence damages only its rect; the background is
	// repainted there via a solid draw instead of the attachment clear.
	c := content
	c.Seq = 2
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= r.width*r.height {
		t.Fatal("expected partial redraw")
	}
	check("partial")
}
