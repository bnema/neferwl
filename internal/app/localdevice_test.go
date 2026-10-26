package app

import (
	"errors"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// A failed lazy open is not cached: the next call retries and its success is
// kept and closed once.
func TestLocalRenderDeviceRetriesAfterFailure(t *testing.T) {
	info := ports.GPUInfo{Path: "card1"}
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	dev := portsmocks.NewMockRenderDevice(t)
	openErr := errors.New("open failed")
	opener.EXPECT().Open(info).Return(nil, openErr).Once()
	opener.EXPECT().Open(info).Return(dev, nil).Once()
	dev.EXPECT().Close().Return().Once()
	l := &localRenderDevice{open: func() (ports.RenderDevice, error) { return opener.Open(info) }}
	if got, err := l.get(); got != nil || !errors.Is(err, openErr) {
		t.Fatalf("first open: %v %v", got, err)
	}
	got, err := l.get()
	if err != nil || got != dev {
		t.Fatalf("second open: %v %v", got, err)
	}
	if again, err := l.get(); err != nil || again != dev {
		t.Fatalf("cached open: %v %v", again, err)
	}
	l.close()
	l.close()
}
