package drm

import (
	"os"
	"slices"
	"testing"
	"unsafe"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestHDRMetadataLayoutAndContent(t *testing.T) {
	var layout hdrOutputMetadata
	if unsafe.Sizeof(layout) != 32 || unsafe.Offsetof(layout.HDMI) != 4 || unsafe.Offsetof(layout.HDMI.DisplayPrimaries) != 2 || unsafe.Offsetof(layout.HDMI.WhitePoint) != 14 || unsafe.Offsetof(layout.HDMI.MaxDisplayMasteringLuminance) != 18 || unsafe.Offsetof(layout.HDMI.MinDisplayMasteringLuminance) != 20 || unsafe.Offsetof(layout.HDMI.MaxCLL) != 22 || unsafe.Offsetof(layout.HDMI.MaxFALL) != 24 {
		t.Fatalf("unexpected HDR metadata layout: size=%d HDMI=%d", unsafe.Sizeof(layout), unsafe.Offsetof(layout.HDMI))
	}
	m := Monitor{Chromaticity: Chromaticity{RedX: 0.64, RedY: 0.33, GreenX: 0.3, GreenY: 0.6, BlueX: 0.15, BlueY: 0.06, WhiteX: 0.3127, WhiteY: 0.329}, HDR: HDRMetadata{MaxLuminance: 1000, MinLuminance: 0.005}}
	data := hdrMetadata(m)
	if len(data.bytes()) != 32 || data.MetadataType != 0 || data.HDMI.EOTF != 2 || data.HDMI.MetadataType != 0 || data.HDMI.DisplayPrimaries != [3][2]uint16{{32000, 16500}, {15000, 30000}, {7500, 3000}} || data.HDMI.WhitePoint != [2]uint16{15635, 16450} || data.HDMI.MaxDisplayMasteringLuminance != 1000 || data.HDMI.MinDisplayMasteringLuminance != 50 || data.HDMI.MaxCLL != 1000 || data.HDMI.MaxFALL != 1000 {
		t.Fatalf("metadata: %+v", data)
	}
	m.HDR.MaxFrameAverage = 400
	if withFALL := hdrMetadata(m); withFALL.HDMI.MaxFALL != 400 {
		t.Fatalf("frame-average luminance: %+v", withFALL)
	}
	if zero := hdrMetadata(Monitor{}); zero.HDMI.MaxCLL != 0 || zero.HDMI.MaxFALL != 0 {
		t.Fatalf("unknown luminance: %+v", zero)
	}
}

func TestHDRModesetPropertiesAndPlaneRefusal(t *testing.T) {
	o, _, _ := testOutput(t)
	o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, DefaultValue: 0, HasDefault: true, MaxBPCValue: 8}
	o.hdr.blob, o.hdr.on = 321, true
	for _, tc := range []struct {
		on                        bool
		bpc, colorspace, metadata uint64
	}{
		{true, 10, 7, 321}, {false, 8, 0, 0},
	} {
		o.hdr.on = tc.on
		req := o.modesetReq(11, true)
		for _, prop := range []struct {
			id   uint32
			want uint64
		}{{102, tc.bpc}, {101, tc.colorspace}, {100, tc.metadata}} {
			if got, ok := req.value(o.conn.id, prop.id); !ok || got != prop.want {
				t.Fatalf("HDR %t prop %d: got %d (%t), want %d", tc.on, prop.id, got, ok, prop.want)
			}
		}
	}
	o.hdr.on = true
	if fb, _, _ := o.scanoutFrame(ports.Scene{}, nil); fb != 0 || o.reason != "no_fullscreen" {
		t.Fatalf("scanout: fb %d reason %q", fb, o.reason)
	}
	if ov, _ := o.overlayFrame(ports.Scene{}, nil); ov.fb != 0 || o.overlayReason != "no_plane" {
		t.Fatalf("overlay: fb %d reason %q", ov.fb, o.overlayReason)
	}
	// Colorspace property without a Default enum must not receive invented 0.
	o.hdr.on = false
	o.hdr.props.HasDefault = false
	if _, ok := o.modesetReq(11, true).value(o.conn.id, o.hdr.props.Colorspace); ok {
		t.Fatal("SDR set Colorspace with no Default enum")
	}
	restore := &atomicReq{}
	o.hdrConnectorProps(restore, false)
	if _, ok := restore.value(o.conn.id, o.hdr.props.Colorspace); ok {
		t.Fatal("restore set Colorspace with no Default enum")
	}
}

func TestHDRTestCommitFallbackReexportsSDR(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL, unix.EINVAL)
	o.cursor = nil
	o.hdr.cap = hdrCapability{Capable: true}
	o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: 203}
	o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, HasDefault: true, MaxBPCValue: 8}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30, Modifier: 0}}
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	var calls []string
	r.EXPECT().SetHDR(float64(203)).Run(func(float64) { calls = append(calls, "hdr") }).Return().Once()
	r.EXPECT().SetHDR(float64(0)).Run(func(float64) { calls = append(calls, "sdr") }).Return().Once()
	r.EXPECT().ExportTargets(2, []uint64{0}).Return([]ports.DMABuf{buf(), buf()}, nil).Twice()
	r.EXPECT().ExportTargets(0, []uint64(nil)).Run(func(int, []uint64) { calls = append(calls, "drop") }).Return(nil, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil)).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(70, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(71, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	k.EXPECT().createBlob(mock.Anything).Return(321, nil).Once()
	k.EXPECT().createBlob(mock.Anything).Return(11, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"hdr", "sdr", "drop"}) {
		t.Fatalf("fallback order: %v", calls)
	}
	if o.hdr.on || !o.hdr.failed {
		t.Fatalf("HDR on %t failed %t", o.hdr.on, o.hdr.failed)
	}
	if len(*commits) != 4 {
		t.Fatalf("commits: %d", len(*commits))
	}
	for i := range 2 {
		if (*commits)[i].flags != atomicTestOnly|atomicAllowModes {
			t.Fatalf("HDR test %d flags: %#x", i, (*commits)[i].flags)
		}
	}
	if (*commits)[2].flags != atomicTestOnly|atomicAllowModes || (*commits)[3].flags != atomicAllowModes {
		t.Fatalf("SDR commits: %#x %#x", (*commits)[2].flags, (*commits)[3].flags)
	}
	if v, _ := (*commits)[0].req.value(o.conn.id, 100); v != 321 {
		t.Fatalf("HDR test metadata: %d", v)
	}
	if v, _ := (*commits)[len(*commits)-1].req.value(o.conn.id, 100); v != 0 {
		t.Fatalf("SDR metadata: %d", v)
	}
}

func TestHDRSettingsRestartComparison(t *testing.T) {
	current := HDRSettings{Enabled: true, SDRBrightness: ports.DefaultSDRBrightness}
	for _, tc := range []struct {
		settings HDRSettings
		restart  bool
	}{
		{HDRSettings{Enabled: true}, false},
		{HDRSettings{Enabled: true, SDRBrightness: 350}, true},
		{HDRSettings{SDRBrightness: ports.DefaultSDRBrightness}, true},
	} {
		if restart := outputNeedsRestart(&Output{hdr: hdrState{settings: current}}, modeInfo{}, tc.settings); restart != tc.restart {
			t.Fatalf("settings %+v: restart %t, want %t", tc.settings, restart, tc.restart)
		}
	}
	if !outputNeedsRestart(&Output{hdr: hdrState{settings: current}}, modeInfo{VRefresh: 60}, current) {
		t.Fatal("mode change did not restart")
	}
}

func TestHDRSuccessfulModeset(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor = nil
	o.hdr.cap = hdrCapability{Capable: true}
	o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: ports.DefaultSDRBrightness}
	o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, HasDefault: true, MaxBPCValue: 8}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30, Modifier: 19}}
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(ports.DefaultSDRBrightness)).Return().Once()
	r.EXPECT().ExportTargets(2, []uint64{19}).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(321, nil).Once()
	k.EXPECT().createBlob(mock.Anything).Return(11, nil).Twice()
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if !o.hdr.on || o.hdr.failed || len(*commits) != 2 {
		t.Fatalf("on=%t failed=%t commits=%d", o.hdr.on, o.hdr.failed, len(*commits))
	}
	for _, c := range *commits {
		for _, tc := range []struct {
			prop uint32
			want uint64
		}{{100, 321}, {101, 7}, {102, 10}} {
			if got, ok := c.req.value(o.conn.id, tc.prop); !ok || got != tc.want {
				t.Fatalf("commit prop %d: %d (%t), want %d", tc.prop, got, ok, tc.want)
			}
		}
	}
}

// The metadata blob is retained across repeated image setup (e.g. VT resume)
// and destroyed only once when Close restores SDR.
func TestHDRBlobReusedAcrossImageSetup(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor = nil
	o.hdr.cap = hdrCapability{Capable: true}
	o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: ports.DefaultSDRBrightness}
	o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, HasDefault: true, MaxBPCValue: 8}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30, Modifier: 19}}
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(ports.DefaultSDRBrightness)).Return().Twice()
	r.EXPECT().ExportTargets(2, []uint64{19}).RunAndReturn(func(int, []uint64) ([]ports.DMABuf, error) { return []ports.DMABuf{buf(), buf()}, nil }).Twice()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	created, destroyed := 0, 0
	k.EXPECT().createBlob(mock.Anything).RunAndReturn(func(data []byte) (uint32, error) {
		if len(data) == 32 {
			created++
			return 321, nil
		}
		return 11, nil
	})
	k.EXPECT().destroyBlob(mock.Anything).RunAndReturn(func(id uint32) error {
		if id == 321 {
			destroyed++
		}
		return nil
	})
	for i := 0; i < 2; i++ {
		if err := o.showImages(r, imagesDriver, nil); err != nil {
			t.Fatal(err)
		}
		if o.hdr.blob != 321 || created != 1 || destroyed != 0 {
			t.Fatalf("pass %d blob %d created %d destroyed %d", i, o.hdr.blob, created, destroyed)
		}
	}
	if len(*commits) != 4 {
		t.Fatalf("commits: %d", len(*commits))
	}
	o.Close()
	if created != 1 || destroyed != 1 {
		t.Fatalf("created %d destroyed %d", created, destroyed)
	}
}

func TestHDRBlobReplacementWaitsForCommit(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	o.hdr.cap = hdrCapability{Capable: true}
	o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: ports.DefaultSDRBrightness}
	o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, HasDefault: true, MaxBPCValue: 8}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30, Modifier: 19}}
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(ports.DefaultSDRBrightness)).Return().Twice()
	r.EXPECT().ExportTargets(2, []uint64{19}).RunAndReturn(func(int, []uint64) ([]ports.DMABuf, error) { return []ports.DMABuf{buf(), buf()}, nil }).Twice()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	metadata := uint32(320)
	k.EXPECT().createBlob(mock.Anything).RunAndReturn(func(data []byte) (uint32, error) {
		if len(data) == 32 {
			metadata++
			return metadata, nil
		}
		return 11, nil
	})
	var destroyed []uint32
	k.EXPECT().destroyBlob(mock.Anything).RunAndReturn(func(id uint32) error {
		if id >= 321 {
			destroyed = append(destroyed, id)
		}
		return nil
	})
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	o.monitor.HDR.MaxLuminance = 1000
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.hdr.blob != 322 || !slices.Equal(destroyed, []uint32{321}) {
		t.Fatalf("blob %d destroyed %v", o.hdr.blob, destroyed)
	}
	o.Close()
	if !slices.Equal(destroyed, []uint32{321, 322}) {
		t.Fatalf("destroyed %v", destroyed)
	}
}

// The metadata blob is reused when unchanged, and a replacement keeps the
// old blob alive until its modeset is committed or rolled back.
func TestHDRBlobSwap(t *testing.T) {
	k := newMockkms(t)
	meta := hdrMetadata(Monitor{HDR: HDRMetadata{MaxLuminance: 1000}})
	other := hdrMetadata(Monitor{HDR: HDRMetadata{MaxLuminance: 600}})
	k.EXPECT().createBlob(mock.Anything).Return(7, nil).Once()
	var h hdrState
	sw, err := h.prepareBlob(k, meta)
	if err != nil || !sw.created || h.blob != 7 {
		t.Fatalf("first blob: %v %+v %d", err, sw, h.blob)
	}
	h.commitBlob(k, sw)

	// Unchanged metadata (VT resume): no new blob.
	if sw, err = h.prepareBlob(k, meta); err != nil || sw.created || h.blob != 7 {
		t.Fatalf("reuse: %v %+v %d", err, sw, h.blob)
	}

	// A refused replacement restores the old blob and frees the new one.
	k.EXPECT().createBlob(mock.Anything).Return(8, nil).Once()
	k.EXPECT().destroyBlob(uint32(8)).Return(nil).Once()
	sw, _ = h.prepareBlob(k, other)
	h.rollbackBlob(k, sw)
	if h.blob != 7 || h.blobData != meta {
		t.Fatalf("rollback: blob %d", h.blob)
	}

	// An accepted replacement frees the old blob.
	k.EXPECT().createBlob(mock.Anything).Return(9, nil).Once()
	k.EXPECT().destroyBlob(uint32(7)).Return(nil).Once()
	sw, _ = h.prepareBlob(k, other)
	h.commitBlob(k, sw)
	if h.blob != 9 || h.blobData != other {
		t.Fatalf("commit: blob %d", h.blob)
	}

	k.EXPECT().destroyBlob(uint32(9)).Return(nil).Once()
	h.releaseBlob(k)
	if h.blob != 0 || h.shown() {
		t.Fatalf("release: blob %d", h.blob)
	}
}
