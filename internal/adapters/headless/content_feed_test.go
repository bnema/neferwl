package headless

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
)

// contentFeed is the wayland side of Run's content input in tests: batches go
// out on ch, and the generated ContentReturn mock checks that each comes back
// reset.
type contentFeed struct {
	ch       chan *domain.ContentBatch
	ret      *portsmocks.MockContentReturn
	returned chan *domain.ContentBatch
}

// newContentFeed makes a feed whose channel holds buffer batches.
func newContentFeed(t *testing.T, buffer int) *contentFeed {
	t.Helper()
	f := &contentFeed{ch: make(chan *domain.ContentBatch, buffer), ret: portsmocks.NewMockContentReturn(t), returned: make(chan *domain.ContentBatch, 64)}
	f.ret.EXPECT().Return(mock.Anything).Run(func(b *domain.ContentBatch) {
		if len(b.Items) != 0 {
			t.Errorf("batch returned with %d items, want it reset", len(b.Items))
		}
		f.returned <- b
	}).Return().Maybe()
	return f
}

// send hands the output one batch holding cs.
func (f *contentFeed) send(cs ...domain.SurfaceContent) {
	f.ch <- &domain.ContentBatch{Items: cs}
}

// in is the channel Run reads.
func (f *contentFeed) in() <-chan *domain.ContentBatch { return f.ch }
