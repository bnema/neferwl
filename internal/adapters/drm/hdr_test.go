package drm

import (
	"testing"
	"unsafe"
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
	if zero := hdrMetadata(Monitor{}); zero.HDMI.MaxCLL != 0 || zero.HDMI.MaxFALL != 0 {
		t.Fatalf("unknown luminance: %+v", zero)
	}
}
