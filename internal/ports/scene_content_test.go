package ports

import "testing"

// HasBuffer is true for content carrying its own buffer; Empty also looks at
// subsurfaces.
func TestSurfaceContentHasBufferEmpty(t *testing.T) {
	tests := []struct {
		name      string
		content   SurfaceContent
		hasBuffer bool
		empty     bool
	}{
		{"zero", SurfaceContent{}, false, true},
		{"shm", SurfaceContent{SHM: &SHMBuffer{}}, true, false},
		{"dmabuf", SurfaceContent{DMABuf: &DMABuf{}}, true, false},
		{"solid", SurfaceContent{Solid: &SolidColor{A: 1}}, true, false},
		{"children only", SurfaceContent{Children: []Subsurface{{}}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.content.HasBuffer(); got != tt.hasBuffer {
				t.Errorf("HasBuffer() = %v, want %v", got, tt.hasBuffer)
			}
			if got := tt.content.Empty(); got != tt.empty {
				t.Errorf("Empty() = %v, want %v", got, tt.empty)
			}
		})
	}
}
