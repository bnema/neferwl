package vulkan

import (
	"encoding/binary"
	"math"
	"testing"
)

// referenceSRGB8 is the per-component formula the table must reproduce.
func referenceSRGB8(bits uint16) byte {
	v := math.Max(0, math.Min(float64(halfFloat(bits)), 1))
	if math.IsNaN(v) {
		return 0
	}
	if v <= .0031308 {
		v *= 12.92
	} else {
		v = 1.055*math.Pow(v, 1/2.4) - .055
	}
	return byte(math.Round(v * 255))
}

func TestHalfToSRGBMatchesFormula(t *testing.T) {
	table := halfToSRGB()
	for i := range 1 << 16 {
		if got, want := table[i], referenceSRGB8(uint16(i)); got != want {
			t.Fatalf("half %#04x: got %d, want %d", i, got, want)
		}
	}
}

func TestHdrRowWritesOpaqueBGRA(t *testing.T) {
	// Pixel 0: red=1.0, green=0.5, blue=0 (half 0x3c00, 0x3800, 0).
	// Pixel 1: negative red, NaN green, +Inf blue, alpha ignored.
	src := make([]byte, 16)
	for i, v := range []uint16{0x3c00, 0x3800, 0, 0x1234, 0xbc00, 0x7e00, 0x7c00, 0} {
		binary.LittleEndian.PutUint16(src[i*2:], v)
	}
	dst := make([]byte, 8)
	hdrRow(dst, src, 2)
	want := []byte{0, referenceSRGB8(0x3800), 255, 255, 255, 0, 0, 255}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("byte %d: got %v, want %v", i, dst, want)
		}
	}
}

func BenchmarkHdrRow(b *testing.B) {
	const n = 5120
	src := make([]byte, n*8)
	for i := range n * 4 {
		binary.LittleEndian.PutUint16(src[i*2:], uint16(i*7919))
	}
	dst := make([]byte, n*4)
	halfToSRGB()
	b.SetBytes(n * 8)
	for b.Loop() {
		hdrRow(dst, src, n)
	}
}
