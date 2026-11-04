package domain

// Cast pixel formats, as DRM fourccs: screencast buffers are dmabufs.
const (
	CastARGB8888    uint32 = 0x34325241 // DRM 'AR24': opaque sRGB8 BGRA
	CastXRGB8888    uint32 = 0x34325258 // DRM 'XR24': sRGB8 BGRX
	CastXBGR2101010 uint32 = 0x30334258 // DRM 'XB30': 10-bit PQ BT.2020, R in bits 0-9
)

var (
	castFormatsSDR = [...]uint32{CastXRGB8888, CastARGB8888}
	castFormatsHDR = [...]uint32{CastXBGR2101010, CastXRGB8888, CastARGB8888}
)

// CastFormats is the formats a screencast offers, preferred first: an HDR
// target offers its PQ signal untouched first and tone-mapped SDR as the
// fallback. The slice is shared: do not modify it.
func CastFormats(hdr bool) []uint32 {
	if hdr {
		return castFormatsHDR[:]
	}
	return castFormatsSDR[:]
}
