package domain

import (
	"os"
	"testing"
)

func TestCaptureRequestOneDestination(t *testing.T) {
	f := os.Stdin // never read or closed: only its presence counts
	buf := &DMABuf{Width: 2, Height: 2, Format: 'X' | 'R'<<8 | '2'<<16 | '4'<<24}
	for name, tc := range map[string]struct {
		req  CaptureRequest
		want bool
	}{
		"none":       {CaptureRequest{}, false},
		"shm":        {CaptureRequest{Dst: SHMBuffer{File: f}}, true},
		"mapped":     {CaptureRequest{Mapped: []byte{0}}, true},
		"dmabuf":     {CaptureRequest{DMABuf: buf}, true},
		"shm+dmabuf": {CaptureRequest{Dst: SHMBuffer{File: f}, DMABuf: buf}, false},
		"mapped+dma": {CaptureRequest{Mapped: []byte{0}, DMABuf: buf}, false},
		"shm+mapped": {CaptureRequest{Dst: SHMBuffer{File: f}, Mapped: []byte{0}}, false},
	} {
		if got := tc.req.OneDestination(); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
