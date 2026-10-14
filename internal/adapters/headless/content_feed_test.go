package headless

import (
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// contentFeed is the wayland side of Run's content input in tests: batches go
// out on ch, and the generated ContentReturn mock checks that each comes back
// reset.
type contentFeed struct {
	ch       chan *ports.ContentBatch
	ret      *portsmocks.MockContentReturn
	returned chan *ports.ContentBatch
}

// newContentFeed makes a feed whose channel holds buffer batches.
func newContentFeed(t *testing.T, buffer int) *contentFeed {
	t.Helper()
	f := &contentFeed{ch: make(chan *ports.ContentBatch, buffer), ret: portsmocks.NewMockContentReturn(t), returned: make(chan *ports.ContentBatch, 64)}
	f.ret.EXPECT().Return(mock.Anything).Run(func(b *ports.ContentBatch) {
		if len(b.Items) != 0 {
			t.Errorf("batch returned with %d items, want it reset", len(b.Items))
		}
		f.returned <- b
	}).Return().Maybe()
	return f
}

// send hands the output one batch holding cs.
func (f *contentFeed) send(cs ...ports.SurfaceContent) {
	f.ch <- &ports.ContentBatch{Items: cs}
}

// in is the channel Run reads.
func (f *contentFeed) in() <-chan *ports.ContentBatch { return f.ch }
