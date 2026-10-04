package vulkan

import (
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// Frames drawn into exported targets reach their dmabufs: another renderer
// importing a target (as KMS scans it out) sees the frame, and readback
// reads the target drawn last. The viewer imports one-plane buffers only,
// so the targets are single-plane here; TestExportTargetsPlanes covers the
// multi-plane (DCC) ones.
func TestExportTargets(t *testing.T) {
	r, err := New(64, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
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
		if err := render(r, ports.Scene{Background: c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := readPixels(t, r).At(3, 3); got != (color.RGBA{0, 0, 255, 255}) {
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
	if got := readPixels(t, viewer).At(10, 10); got != (color.RGBA{255, 0, 0, 255}) {
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
	// GPU tests and headless --screenshot-raw (SetHDRReadback); never DRM.
	r.SetHDRReadback(true)
	if r.physical == 0 {
		t.Skip("no exportable GPU")
	}
	r.hdrMods = r.probeModifiers(r.physical, vk.FormatA2r10g10b10UnormPack32)
	if len(r.hdrMods) == 0 {
		t.Skip("no HDR modifier supporting transfer-src")
	}
	bufs, err := r.ExportTargets(1, r.hdrMods, false)
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
	if got := readPixels(t, r).RGBAAt(0, 0); got.R != 128 || got.G != 128 || got.B != 128 {
		t.Fatalf("SDR readback: %v", got)
	}
}

// A virtual (headless) HDR output has no display to list modifiers: the
// export accepts every exportable one. A DRM output (no SetVirtualOutput)
// still refuses an unspecified list.
func TestVirtualHDRExportsWithoutDisplayList(t *testing.T) {
	r, err := New(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if r.physical == 0 || len(r.hdrMods) == 0 {
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
	r := &Renderer{renderMods: got, modPlanes: planes}
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
	r, err := New(64, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	closeAll := func(bufs []ports.DMABuf) {
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
	if len(one[0].Planes) != 1 || r.modPlanes[one[0].Modifier] > 1 {
		t.Fatalf("single-plane export has %d planes (modifier %#x)", len(one[0].Planes), one[0].Modifier)
	}
	closeAll(one)
	multi := false
	for _, m := range r.renderMods {
		multi = multi || r.modPlanes[m] > 1
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
		want := max(r.modPlanes[b.Modifier], 1)
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
		if err := render(r, ports.Scene{Background: tc.bg}, nil); err != nil {
			t.Fatal(err)
		}
		if got := readPixels(t, r).At(3, 3); got != tc.want {
			t.Fatalf("target %d (modifier %#x): read back %v, want %v", i, bufs[i].Modifier, got, tc.want)
		}
	}
}
