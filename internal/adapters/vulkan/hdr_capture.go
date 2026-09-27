package vulkan

import (
	"encoding/binary"
	"math"
	"unsafe"
)

// hdrPixel converts one RGBA16F linear-light pixel to clipped BGRA8 at
// readback time only. No conversion work is done on normal presentation.
func (r *Renderer) hdrPixel(x, y int) [4]byte {
	src := unsafe.Slice((*byte)(r.mapped), r.width*r.height*8)
	pos := (y*r.width + x) * 8
	var out [4]byte
	for i := 0; i < 3; i++ {
		v := math.Max(0, math.Min(float64(halfFloat(binary.LittleEndian.Uint16(src[pos+i*2:]))), 1))
		if v <= .0031308 {
			v *= 12.92
		} else {
			v = 1.055*math.Pow(v, 1/2.4) - .055
		}
		out[2-i] = byte(math.Round(v * 255))
	}
	out[3] = 255
	return out
}

// halfFloat expands an IEEE 754 binary16 component for capture only.
func halfFloat(bits uint16) float32 {
	sign := uint32(bits&0x8000) << 16
	exponent := uint32(bits>>10) & 31
	mantissa := uint32(bits & 1023)
	var value uint32
	switch exponent {
	case 0:
		if mantissa == 0 {
			value = sign
		} else {
			e := uint32(113)
			for mantissa&0x400 == 0 {
				mantissa <<= 1
				e--
			}
			value = sign | e<<23 | (mantissa&1023)<<13
		}
	case 31:
		value = sign | 0x7f800000 | mantissa<<13
	default:
		value = sign | (exponent+112)<<23 | mantissa<<13
	}
	return math.Float32frombits(value)
}
