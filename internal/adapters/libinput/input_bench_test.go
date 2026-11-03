package libinput

import (
	"bnema.dev/zerowrap"
	"fmt"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

// Measures the pure relative-motion translation and production coalescer,
// excluding native libinput calls and kernel event delivery.
func BenchmarkTranslateCoalesce(b *testing.B) {
	for _, devices := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("devices=%d", devices), func(b *testing.B) {
			p := newPointer(domain.Layout{{Width: 800, Height: 600, Scale: 1}})
			for i := 1; i <= devices; i++ {
				p.addDevice(uintptr(i), domain.InputDeviceID(i))
			}
			f := newForwarder(zerowrap.Default())
			f.queue = make([]ports.InputEvent, 0, 256)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := domain.InputDeviceID(i%devices + 1)
				m := p.forDevice(uintptr(id)).move(0.1, 0)
				m.Device = id
				f.push(m)
				if i%128 == 127 {
					f.queue = f.queue[:0]
				}
			}
		})
	}
}
