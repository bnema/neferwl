package vulkan

import (
	"errors"
	"image/color"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Frames drawn into exported targets reach their dmabufs: another renderer
// importing a target (as KMS scans it out) sees the frame, and readback
// reads the target drawn last. The viewer imports one-plane buffers only,
// so the targets are single-plane here; TestExportTargetsPlanes covers the
// multi-plane (DCC) ones.
func TestExportTargets(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	bufs, err := r.ExportTargets(2, nil, true)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	defer func() {
		for _, b := range bufs {
			for _, p := range b.Planes {
				p.File.Close()
			}
		}
	}()
	if len(bufs) != 2 || bufs[0].Width != 64 || bufs[0].Format != fourccXRGB || bufs[0].Planes[0].Stride < 64*4 {
		t.Fatalf("targets %+v", bufs)
	}
	colors := []string{"#ff0000", "#0000ff"}
	for i, c := range colors {
		r.UseTarget(i)
		if err := render(r, domain.Scene{Background: c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := readPixels(t, r).At(3, 3); got != (color.RGBA{0, 0, 255, 255}) {
		t.Fatalf("readback of the last target %v", got)
	}
	// Import target 0 as a client buffer, like the display would.
	viewer := newTestRenderer(t, 64, 32)
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
	scene := domain.Scene{Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 32}}}}
	contents := map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Width: 64, Height: 32, Opaque: true, DMABuf: &b}}
	if err := render(viewer, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, viewer).At(10, 10); got != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("target 0 as seen by the display %v", got)
	}
}

// The exported HDR image carries PQ while Pixels remains the composed SDR
// image. A test-only transfer-src readback checks the actual shader output.
func TestHDRExportTarget(t *testing.T) {
	r := newTestRenderer(t, 32, 16)
	defer r.Close()
	r.SetHDR(203)
	// GPU tests and headless --screenshot-raw (SetHDRReadback); never DRM.
	r.SetHDRReadback(true)
	if r.gpu.physical == 0 {
		t.Skip("no exportable GPU")
	}
	mods := r.gpu.hdrReadbackMods
	if len(r.gpu.hdrReadbackMods) == 0 {
		t.Skip("no HDR modifier supporting transfer-src")
	}
	bufs, err := r.ExportTargets(1, mods, false)
	if err != nil {
		t.Skipf("no HDR-exportable target: %v", err)
	}
	defer bufs[0].Planes[0].File.Close()
	if bufs[0].Format != fourccXR30 {
		t.Fatalf("format: %#x", bufs[0].Format)
	}

	// A real second frame with an unchanged scene must retain the internal
	// image and use a partial-damage load from its actual prior layout.
	scene := domain.Scene{Seq: 1, Background: "#ffffff"}
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
	if got := readPixels(t, r).RGBAAt(0, 0); got.R != 128 || got.G != 128 || got.B != 128 {
		t.Fatalf("SDR readback: %v", got)
	}
}

// A virtual (headless) HDR output has no display to list modifiers: the
// export accepts every exportable one. A DRM output (no SetVirtualOutput)
// still refuses an unspecified list.
func TestVirtualHDRExportsWithoutDisplayList(t *testing.T) {
	r := newTestRenderer(t, 64, 16)
	defer r.Close()
	if r.gpu.physical == 0 || len(r.gpu.hdrReadbackMods) == 0 {
		t.Skip("no exportable HDR GPU")
	}
	r.SetHDR(203)
	if bufs, err := r.ExportTargets(1, nil, false); err == nil {
		bufs[0].Planes[0].File.Close()
		t.Fatal("HDR export without a display list succeeded on a non-virtual output")
	}
	r.SetVirtualOutput(true)
	bufs, err := r.ExportTargets(1, nil, false)
	if err != nil {
		t.Fatalf("virtual HDR export: %v", err)
	}
	defer bufs[0].Planes[0].File.Close()
	if bufs[0].Format != fourccXR30 {
		t.Fatalf("format: %#x", bufs[0].Format)
	}
}

// Test-only target readback: production HDR images do not have transfer-src.
func checkHDRPixel(t *testing.T, r *Renderer, rgb [3]float64) {
	t.Helper()
	v := pqTargetWords(t, r)[0]
	red, green, blue := hdrPixel(rgb[0], rgb[1], rgb[2], 203)
	for i, want := range []float64{blue, green, red} {
		got := float64(v>>uint(i*10)&1023) / 1023
		if math.Abs(got-want) > 0.005 {
			t.Fatalf("rgb %v channel %d: PQ %.5f want %.5f", rgb, i, got, want)
		}
	}
}

// Targets may use DCC modifiers: one to four memory planes are kept and
// their counts recorded; singlePlane drops the multi-plane ones.
func TestFilterModifiersKeepsMultiPlane(t *testing.T) {
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit)
	mods := []vk.DrmFormatModifierPropertiesEXT{
		{DrmFormatModifier: 0, DrmFormatModifierPlaneCount: 1, DrmFormatModifierTilingFeatures: need},
		{DrmFormatModifier: 10, DrmFormatModifierPlaneCount: 3, DrmFormatModifierTilingFeatures: need},
		{DrmFormatModifier: 11, DrmFormatModifierPlaneCount: 4, DrmFormatModifierTilingFeatures: need},
		{DrmFormatModifier: 12, DrmFormatModifierPlaneCount: 5, DrmFormatModifierTilingFeatures: need},
		{DrmFormatModifier: 13, DrmFormatModifierPlaneCount: 0, DrmFormatModifierTilingFeatures: need},
		{DrmFormatModifier: 14, DrmFormatModifierPlaneCount: 2},
		{DrmFormatModifier: 15, DrmFormatModifierPlaneCount: 2, DrmFormatModifierTilingFeatures: need},
	}
	got, planes := filterModifiers(mods, need, func(m uint64) bool { return m != 15 })
	if !slices.Equal(got, []uint64{0, 10, 11}) {
		t.Fatalf("kept %v", got)
	}
	if planes[0] != 1 || planes[10] != 3 || planes[11] != 4 || len(planes) != 3 {
		t.Fatalf("planes %v", planes)
	}
	r := &Renderer{gpu: &GPU{renderMods: got, modPlanes: planes}}
	if all := r.exportModifiers(nil, false); !slices.Equal(all, []uint64{0, 10, 11}) {
		t.Fatalf("multi-plane export %v", all)
	}
	if one := r.exportModifiers(nil, true); !slices.Equal(one, []uint64{0}) {
		t.Fatalf("single-plane export %v", one)
	}
	if one := r.exportModifiers([]uint64{10, 11}, true); len(one) != 0 {
		t.Fatalf("single-plane with only DCC offered: %v", one)
	}
	if two := r.exportModifiers([]uint64{10, 0}, false); !slices.Equal(two, []uint64{0, 10}) {
		t.Fatalf("display intersection %v", two)
	}
}

// Every exported target carries one file per memory plane of its modifier
// (DCC modifiers have two to four), each with a layout, and drawing into
// each target works: the frames read back through the producing renderer.
// A multi-plane image cannot be read back through a viewer import, which
// only imports one-plane buffers.
func TestExportTargetsPlanes(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	closeAll := func(bufs []domain.DMABuf) {
		for _, b := range bufs {
			for _, p := range b.Planes {
				p.File.Close()
			}
		}
	}
	// Single-plane export never returns a multi-plane modifier.
	one, err := r.ExportTargets(1, nil, true)
	if err != nil {
		t.Skipf("no single-plane targets: %v", err)
	}
	if len(one[0].Planes) != 1 || r.gpu.modPlanes[one[0].Modifier] > 1 {
		t.Fatalf("single-plane export has %d planes (modifier %#x)", len(one[0].Planes), one[0].Modifier)
	}
	closeAll(one)
	multi := false
	for _, m := range r.gpu.renderMods {
		multi = multi || r.gpu.modPlanes[m] > 1
	}
	if !multi {
		t.Skip("device has no multi-plane target modifier")
	}
	bufs, err := r.ExportTargets(2, nil, false)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	defer closeAll(bufs)
	for _, b := range bufs {
		want := max(r.gpu.modPlanes[b.Modifier], 1)
		if uint32(len(b.Planes)) != want {
			t.Fatalf("modifier %#x: %d plane files, want %d", b.Modifier, len(b.Planes), want)
		}
		seen := map[int]bool{}
		for i, p := range b.Planes {
			if p.File == nil || p.Stride == 0 {
				t.Fatalf("modifier %#x plane %d: %+v", b.Modifier, i, p)
			}
			// The planes share one memory object: planes 1.. lie after plane 0.
			if i > 0 && p.Offset == 0 {
				t.Fatalf("modifier %#x plane %d at offset 0", b.Modifier, i)
			}
			if seen[int(p.File.Fd())] {
				t.Fatalf("modifier %#x: plane %d shares a file", b.Modifier, i)
			}
			seen[int(p.File.Fd())] = true
		}
		t.Logf("modifier %#x planes %d", b.Modifier, len(b.Planes))
	}
	for i, tc := range []struct {
		bg   string
		want color.RGBA
	}{{"#ff0000", color.RGBA{255, 0, 0, 255}}, {"#0000ff", color.RGBA{0, 0, 255, 255}}} {
		r.UseTarget(i)
		if err := render(r, domain.Scene{Background: tc.bg}, nil); err != nil {
			t.Fatal(err)
		}
		if got := readPixels(t, r).At(3, 3); got != tc.want {
			t.Fatalf("target %d (modifier %#x): read back %v, want %v", i, bufs[i].Modifier, got, tc.want)
		}
	}
}

// A target exported for a display modifier list carries one of its
// modifiers, whichever they are: KMS refuses a framebuffer whose modifier
// the plane does not advertise, so the intersection must hold on the real
// driver (RADV also exports DCC variants displays do not list).
func TestExportTargetsStayInTheDisplayList(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	if len(r.gpu.renderMods) == 0 {
		t.Skip("device exports no render modifier")
	}
	for _, m := range r.gpu.renderMods {
		bufs, err := r.ExportTargets(1, []uint64{m}, false)
		if err != nil {
			t.Fatalf("modifier %#x is exportable but export failed: %v", m, err)
		}
		got := bufs[0].Modifier
		for _, p := range bufs[0].Planes {
			p.File.Close()
		}
		if got != m {
			t.Fatalf("display lists only %#x, target has modifier %#x", m, got)
		}
	}
	// A list with no exportable modifier is an error, not a guess.
	if bufs, err := r.ExportTargets(1, []uint64{0x1234567}, false); err == nil {
		for _, p := range bufs[0].Planes {
			p.File.Close()
		}
		t.Fatalf("export with an unexportable display list gave modifier %#x", bufs[0].Modifier)
	}
}

// The AMD DCC retile modifiers cost a compute pass per frame: they are never
// targets, whatever the display lists. Plain DCC (pipe-aligned or not), the
// non-DCC tiled modifier and non-AMD modifiers with bit 14 set stay.
func TestFilterModifiersDropsDCCRetile(t *testing.T) {
	const (
		retile       = 0x200000010567b03  // AMD DCC, DCC_RETILE (laptop RADV)
		pipeAligned  = 0x20000000056bb03  // AMD DCC, pipe-aligned
		unaligned    = 0x200000000563b03  // AMD DCC, not pipe-aligned
		tiled        = 0x200000010401b03  // AMD tiled, no DCC
		foreignRetil = 0x0100000000004003 // vendor 1, bit 14 set: not AMD's
	)
	need := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit)
	var mods []vk.DrmFormatModifierPropertiesEXT
	for _, m := range []uint64{retile, pipeAligned, unaligned, tiled, foreignRetil, 0} {
		planes := uint32(1)
		if m == retile || m == pipeAligned || m == unaligned {
			planes = 3
		}
		mods = append(mods, vk.DrmFormatModifierPropertiesEXT{DrmFormatModifier: m, DrmFormatModifierPlaneCount: planes, DrmFormatModifierTilingFeatures: need})
	}
	got, planes := filterModifiers(mods, need, func(uint64) bool { return true })
	if want := []uint64{pipeAligned, unaligned, tiled, foreignRetil, 0}; !slices.Equal(got, want) {
		t.Fatalf("kept %#x, want %#x", got, want)
	}
	if _, ok := planes[retile]; ok {
		t.Fatalf("retile modifier has a plane count: %v", planes)
	}
	r := &Renderer{gpu: &GPU{renderMods: got, modPlanes: planes}}
	if out := r.exportModifiers([]uint64{retile, tiled}, false); !slices.Equal(out, []uint64{tiled}) {
		t.Fatalf("display listing the retile modifier: %#x", out)
	}
	if out := r.exportModifiers([]uint64{retile}, false); len(out) != 0 {
		t.Fatalf("display listing only the retile modifier: %#x", out)
	}
}

// The device's exportable modifiers hold no AMD DCC retile one, whatever
// RADV lists (it lists them on GPUs with several render backends).
func TestProbedModifiersHaveNoDCCRetile(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	for _, m := range append(slices.Clone(r.gpu.renderMods), r.gpu.hdrMods...) {
		if retilesDCC(m) {
			t.Fatalf("target modifier %#x retiles DCC", m)
		}
	}
}

// pipeFile is a file for plane tests: a distinct inode per call.
func pipeFile(t *testing.T) *os.File {
	t.Helper()
	f, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { f.Close() })
	return f
}

// Planes must be in one dmabuf: the same file or duplicates of it are, two
// files are not. No device needed.
func TestSameDMABuf(t *testing.T) {
	one, other := pipeFile(t), pipeFile(t)
	for _, tc := range []struct {
		name   string
		planes []domain.DMABufPlane
		ok     bool
	}{
		{"one plane", []domain.DMABufPlane{{File: one}}, true},
		{"same file twice", []domain.DMABufPlane{{File: one}, {File: one}}, true},
		{"two files", []domain.DMABufPlane{{File: one}, {File: other}}, false},
		{"third plane elsewhere", []domain.DMABufPlane{{File: one}, {File: one}, {File: other}}, false},
		{"no file", []domain.DMABufPlane{{File: one}, {}}, false},
	} {
		err := sameDMABuf(tc.planes)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
		if err != nil && tc.name != "no file" && !errors.Is(err, errImageDMABuf) {
			t.Errorf("%s: %v is not errImageDMABuf", tc.name, err)
		}
	}
}

// TargetModifiers keeps the modifiers the display lists and drops
// multi-plane ones for singlePlane.
func TestTargetModifiersFiltering(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	mods := r.TargetModifiers(nil, false)
	if len(mods) == 0 {
		t.Skip("no importable target modifier")
	}
	if got := r.TargetModifiers([]uint64{0x1234567}, false); len(got) != 0 {
		t.Errorf("modifiers outside the display list: %#x", got)
	}
	if got := r.TargetModifiers(mods[:1], false); len(got) != 1 || got[0] != mods[0] {
		t.Errorf("display list %#x gave %#x", mods[:1], got)
	}
	for _, m := range r.TargetModifiers(nil, true) {
		if r.gpu.modPlanes[m] > 1 {
			t.Errorf("single-plane list holds multi-plane modifier %#x", m)
		}
	}
	// The answer is probed once and kept.
	if len(r.gpu.importMods) == 0 {
		t.Error("import modifiers not cached")
	}
	again := r.TargetModifiers(nil, false)
	if !slices.Equal(mods, again) {
		t.Errorf("cached list differs: %#x vs %#x", mods, again)
	}
}

// ImportTargets refuses an image that does not fit the renderer before
// asking the driver, says why, and leaves no targets: frames go back to the
// renderer's own image.
func TestImportTargetsRefusesMismatchedImages(t *testing.T) {
	r := newTestRenderer(t, 64, 32)
	defer r.Close()
	var single uint64
	found := false
	for _, m := range r.TargetModifiers(nil, true) {
		single, found = m, true
		break
	}
	if !found {
		t.Skip("no importable one-plane target modifier")
	}
	good := func() domain.DMABuf {
		return domain.DMABuf{Width: 64, Height: 32, Format: fourccXRGB, Modifier: single, Planes: []domain.DMABufPlane{{File: pipeFile(t)}}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.DMABuf)
		want   error
	}{
		{"width", func(b *domain.DMABuf) { b.Width = 65 }, errImageGeometry},
		{"height", func(b *domain.DMABuf) { b.Height = 31 }, errImageGeometry},
		{"format", func(b *domain.DMABuf) { b.Format = fourccXR30 }, errImageGeometry},
		{"unknown modifier", func(b *domain.DMABuf) { b.Modifier = 0x1234567 }, errImageModifier},
		{"no planes", func(b *domain.DMABuf) { b.Planes = nil }, errImagePlanes},
		{"too many planes", func(b *domain.DMABuf) {
			b.Planes = append(b.Planes, domain.DMABufPlane{File: pipeFile(t)})
		}, errImagePlanes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := good()
			tc.mutate(&b)
			err := r.ImportTargets([]domain.DMABuf{b})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			if len(r.targets) != 0 {
				t.Fatalf("%d targets left", len(r.targets))
			}
		})
	}
	t.Run("second image refused drops the first", func(t *testing.T) {
		// A real dmabuf for the first image: the renderer's own export.
		exported, err := r.ExportTargets(1, []uint64{single}, true)
		if err != nil {
			t.Skipf("no exportable target to reuse: %v", err)
		}
		defer closeBufs(exported)
		bad := good()
		bad.Width = 1
		err = r.ImportTargets([]domain.DMABuf{exported[0], bad})
		if !errors.Is(err, errImageGeometry) {
			t.Fatalf("error %v, want the second image's geometry refusal", err)
		}
		if len(r.targets) != 0 {
			t.Fatalf("%d targets left", len(r.targets))
		}
	})
	t.Run("dropping", func(t *testing.T) {
		if err := r.ImportTargets(nil); err != nil || len(r.targets) != 0 {
			t.Fatalf("%v, %d targets", err, len(r.targets))
		}
	})
	t.Run("planes in different dmabufs", func(t *testing.T) {
		multi := uint64(0)
		for _, m := range r.TargetModifiers(nil, false) {
			if r.gpu.modPlanes[m] > 1 {
				multi = m
				break
			}
		}
		if multi == 0 {
			t.Skip("no multi-plane importable modifier on this device: the check is covered by TestSameDMABuf")
		}
		b := good()
		b.Modifier = multi
		b.Planes = nil
		for range r.gpu.modPlanes[multi] {
			b.Planes = append(b.Planes, domain.DMABufPlane{File: pipeFile(t)})
		}
		if err := r.ImportTargets([]domain.DMABuf{b}); !errors.Is(err, errImageDMABuf) {
			t.Fatalf("error %v", err)
		}
	})
}

func closeBufs(bufs []domain.DMABuf) {
	for _, b := range bufs {
		for _, p := range b.Planes {
			p.File.Close()
		}
	}
}
