package headless

import (
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// presentedSink is a generated PresentationSink mock that records every push
// on the returned channel. A full channel drops the report: Push never blocks,
// as the port requires.
func presentedSink(t *testing.T, size int) (ports.PresentationSink, chan ports.OutputPresented) {
	t.Helper()
	ch := make(chan ports.OutputPresented, size)
	sink := portsmocks.NewMockPresentationSink(t)
	sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r ports.OutputPresented) {
		select {
		case ch <- r:
		default:
		}
	}).Maybe()
	return sink, ch
}
