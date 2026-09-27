package vulkan

import (
	"encoding/binary"
	"math"
	"sync"
	"unsafe"
)

// halfToSRGB maps every binary16 bit pattern to its clipped sRGB byte.
// It is built on the first HDR capture; normal presentation never touches it.
var halfToSRGB = sync.OnceValue(func() *[1 << 16]byte {
	var t [1 << 16]byte
	for i := range t {
		t[i] = encodeSRGB8(halfFloat(uint16(i)))
	}
	return &t
})

// encodeSRGB8 clips a linear-light value to [0, 1] and encodes it as sRGB.
func encodeSRGB8(f float32) byte {
	v := float64(f)
	if !(v > 0) { // also maps NaN to black
		return 0
	}
	v = math.Min(v, 1)
	if v <= .0031308 {
		v *= 12.92
	} else {
		v = 1.055*math.Pow(v, 1/2.4) - .055
	}
	return byte(math.Round(v * 255))
}

// hdrRow converts n RGBA16F linear-light pixels to opaque BGRA8 sRGB.
func hdrRow(dst, src []byte, n int) {
	t := halfToSRGB()
	_ = dst[n*4-1]
	_ = src[n*8-1]
	for x := range n {
		s, d := src[x*8:x*8+6], dst[x*4:x*4+4]
		d[0] = t[binary.LittleEndian.Uint16(s[4:])]
		d[1] = t[binary.LittleEndian.Uint16(s[2:])]
		d[2] = t[binary.LittleEndian.Uint16(s[0:])]
		d[3] = 255
	}
}

// hdrRegion converts a region of the mapped HDR readback, row by row.
// swapRB writes RGBA instead of BGRA.
func (r *Renderer) hdrRegion(dst []byte, stride, x0, y0, w, h int, swapRB bool) {
	src := unsafe.Slice((*byte)(r.mapped), r.width*r.height*8)
	for y := range h {
		start := ((y0+y)*r.width + x0) * 8
		row := dst[y*stride : y*stride+w*4]
		hdrRow(row, src[start:start+w*8], w)
		if swapRB {
			for i := 0; i < len(row); i += 4 {
				row[i], row[i+2] = row[i+2], row[i]
			}
		}
	}
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
