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

// hdrState is an output's HDR10 signal: what the display can do, what the
// config asks, and the metadata blob KMS shows. The output goroutine owns it.
type hdrState struct {
	cap      hdrCapability
	props    connectorHDRProps
	settings HDRSettings
	// failed disables retries until this Output is replaced; on is the
	// currently selected signal encoding, including on VT resume.
	on, failed bool
	// blob is the live metadata blob and blobData its content. A blob stays
	// alive while a modeset may still show it.
	blob     uint32
	blobData hdrOutputMetadata
}

// wanted reports whether the next modeset should try HDR10.
func (h *hdrState) wanted() bool {
	return h.settings.Enabled && h.cap.Capable && !h.failed
}

// shown reports whether an HDR modeset may still be on screen: a live blob
// counts even after a failed SDR fallback (on is then false).
func (h *hdrState) shown() bool { return h.on || h.blob != 0 }

// hdrBlobSwap is a metadata blob replacement waiting for its modeset.
type hdrBlobSwap struct {
	old     uint32
	meta    hdrOutputMetadata
	created bool
}

// prepareBlob makes h.blob carry meta. The live blob is reused when its
// content matches (VT resume); otherwise a replacement is created and the
// old one stays alive until the swap is committed or rolled back.
func (h *hdrState) prepareBlob(k kms, meta hdrOutputMetadata) (hdrBlobSwap, error) {
	sw := hdrBlobSwap{old: h.blob, meta: meta}
	if sw.old != 0 && meta == h.blobData {
		return sw, nil
	}
	blob, err := k.createBlob(meta.bytes())
	if err != nil {
		return sw, err
	}
	h.blob, sw.created = blob, true
	return sw, nil
}

// commitBlob runs once KMS accepted the modeset: the old blob is freed.
func (h *hdrState) commitBlob(k kms, sw hdrBlobSwap) {
	if !sw.created {
		return
	}
	h.blobData = sw.meta
	if sw.old != 0 {
		_ = k.destroyBlob(sw.old)
	}
}

// rollbackBlob restores the old blob after a refused modeset: the existing
// modeset may still use it. Without an old blob the new one stays live.
func (h *hdrState) rollbackBlob(k kms, sw hdrBlobSwap) {
	if !sw.created {
		return
	}
	if sw.old != 0 {
		_ = k.destroyBlob(h.blob)
		h.blob = sw.old
		return
	}
	h.blobData = sw.meta
}

// releaseBlob frees the blob once no modeset can show it.
func (h *hdrState) releaseBlob(k kms) {
	if h.blob != 0 {
		_ = k.destroyBlob(h.blob)
	}
	h.blob, h.blobData = 0, hdrOutputMetadata{}
}

// hdrConnectorProps sets the connector's signal metadata on a modeset or restore.
func (o *Output) hdrConnectorProps(req *atomicReq, enabled bool) {
	p := o.hdr.props
	if enabled {
		req.set(o.conn.id, p.MaxBPC, 10)
		req.set(o.conn.id, p.Colorspace, p.BT2020Value)
		req.set(o.conn.id, p.Metadata, uint64(o.hdr.blob))
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
