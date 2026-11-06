package supervisor

import (
	"sync"
	"testing"
	"testing/synctest"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/require"
)

func TestRenderDeviceConcurrentPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := portsmocks.NewMockRenderDevice(t)
		b := portsmocks.NewMockRenderDevice(t)
		backend := &Backend{}
		require.Nil(t, backend.RenderDevice())
		backend.setRender(a)
		require.Same(t, a, backend.RenderDevice())
		start := make(chan struct{})
		var readers sync.WaitGroup
		for range 4 {
			readers.Go(func() {
				<-start
				for range 10000 {
					dev := backend.RenderDevice()
					if dev != nil && dev != a && dev != b {
						t.Error("unexpected published render device")
						return
					}
				}
			})
		}
		close(start)
		for range 10000 {
			backend.setRender(nil)
			backend.setRender(b)
			backend.setRender(a)
		}
		readers.Wait()
		backend.setRender(nil)
		require.Nil(t, backend.RenderDevice())
		backend.setRender(b)
		require.Same(t, b, backend.RenderDevice())
	})
}
