package domain

// Cast pixel formats, as wl_shm/DRM format codes.
const (
	CastARGB8888    uint32 = 0          // wl_shm ARGB8888
	CastXRGB8888    uint32 = 1          // wl_shm XRGB8888
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
