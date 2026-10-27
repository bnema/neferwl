package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// presentedSink is a generated PresentationSink mock that records every push
// on the returned channel. A full channel drops the report: Push never blocks,
// as the port requires.
func presentedSink(t *testing.T, size int) (ports.PresentationSink, chan domain.OutputPresented) {
	t.Helper()
	ch := make(chan domain.OutputPresented, size)
	sink := portsmocks.NewMockPresentationSink(t)
	sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r domain.OutputPresented) {
		select {
		case ch <- r:
		default:
		}
	}).Maybe()
	return sink, ch
}

// discardSink is a PresentationSink for tests that ignore the reports.
func discardSink(t *testing.T) ports.PresentationSink {
	t.Helper()
	sink, _ := presentedSink(t, 1)
	return sink
}
