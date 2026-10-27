package domain

// SurfaceColor is the committed encoding of a surface. Zero means sRGB.
// PQ+BT.2020 and extended-linear sRGB are color-managed; zero is legacy
// sRGB. MaxCLL/MaxFALL are in nits. YUV metadata is per surface commit.
type SurfaceColor struct {
	TF, Primaries   uint8
	MaxCLL, MaxFALL uint16
	// Coefficients/range follow wp_color_representation_v1 (zero =
	// unspecified, for YUV interpreted as BT.709 limited). Chroma is a
	// H.273 Chroma420SampleLocType + 1 (zero defaults to type 0).
	Coefficients, Range, Chroma uint8
}

const (
	ColorTFPQ             uint8 = 11 // wp_color_manager_v1 ST2084 PQ
	ColorTFExtendedLinear uint8 = 5  // wp_color_manager_v1 ext_linear
	ColorPrimariesBT2020  uint8 = 6  // wp_color_manager_v1 BT.2020
	ColorPrimariesSRGB    uint8 = 1
	ColorTFSRGB           uint8 = 9 // wp_color_manager_v1 sRGB
)

func (c SurfaceColor) IsPQ2020() bool {
	return c.TF == ColorTFPQ && c.Primaries == ColorPrimariesBT2020
}

func (c SurfaceColor) IsExtendedLinear() bool {
	return c.TF == ColorTFExtendedLinear && c.Primaries == ColorPrimariesSRGB
}

// SolidColor is the color of a single-pixel buffer: premultiplied by alpha,
// 0 to 1, sRGB-encoded like the pixels of client buffers.
type SolidColor struct{ R, G, B, A float32 }
