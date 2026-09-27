package drm

import (
	"math"
	"unsafe"
)

// HDMI static metadata type 1, embedded in struct hdr_output_metadata.
// All coordinates use 0.00002; minimum luminance uses 0.0001 nits.
type hdmiMetadataType1 struct {
	EOTF, MetadataType           uint8
	DisplayPrimaries             [3][2]uint16
	WhitePoint                   [2]uint16
	MaxDisplayMasteringLuminance uint16
	MinDisplayMasteringLuminance uint16
	MaxCLL, MaxFALL              uint16
}

type hdrOutputMetadata struct {
	MetadataType uint32
	HDMI         hdmiMetadataType1
}

func hdrMetadata(m Monitor) hdrOutputMetadata {
	unit := func(v, scale float64) uint16 {
		return uint16(math.Round(max(0, min(v*scale, math.MaxUint16))))
	}
	c, h := m.Chromaticity, m.HDR
	return hdrOutputMetadata{HDMI: hdmiMetadataType1{
		EOTF:                         2,
		DisplayPrimaries:             [3][2]uint16{{unit(c.RedX, 50000), unit(c.RedY, 50000)}, {unit(c.GreenX, 50000), unit(c.GreenY, 50000)}, {unit(c.BlueX, 50000), unit(c.BlueY, 50000)}},
		WhitePoint:                   [2]uint16{unit(c.WhiteX, 50000), unit(c.WhiteY, 50000)},
		MaxDisplayMasteringLuminance: unit(h.MaxLuminance, 1),
		MinDisplayMasteringLuminance: unit(h.MinLuminance, 10000),
		MaxCLL:                       unit(h.MaxLuminance, 1), MaxFALL: unit(maxFrameAverage(h), 1),
	}}
}

func maxFrameAverage(h HDRMetadata) float64 {
	if h.MaxFrameAverage > 0 {
		return h.MaxFrameAverage
	}
	return h.MaxLuminance
}

// hdrConnectorProps sets the connector's signal metadata on a modeset or restore.
func (o *Output) hdrConnectorProps(req *atomicReq, enabled bool) {
	p := o.hdrProps
	if enabled {
		req.set(o.conn.id, p.MaxBPC, 10)
		req.set(o.conn.id, p.Colorspace, p.BT2020Value)
		req.set(o.conn.id, p.Metadata, uint64(o.hdrBlob))
		return
	}
	req.set(o.conn.id, p.MaxBPC, p.MaxBPCValue)
	if p.HasDefault {
		req.set(o.conn.id, p.Colorspace, p.DefaultValue)
	}
	req.set(o.conn.id, p.Metadata, 0)
}

func (m *hdrOutputMetadata) bytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(m)), unsafe.Sizeof(*m))
}
