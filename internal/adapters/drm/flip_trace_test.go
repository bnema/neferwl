package drm

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
)

// Only the event of our pending commit is traced: a stale event and the
// event that ends an EBUSY wait are not.
func TestTraceFlipsOnlyOurCommit(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor = nil
	o.traceFlips = true
	var log bytes.Buffer
	o.log = zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &log})
	seen := map[ports.WindowID]uint64{}
	flips := func() int { return strings.Count(log.String(), `"message":"flip"`) }

	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	stale := flipEvent{crtc: tCrtc, user: c.user + 1<<userKindBits}
	if o.completed(stale, seen) || flips() != 0 {
		t.Fatalf("stale event traced: %s", log.String())
	}
	if !o.completed(flipEvent{crtc: tCrtc, user: c.user, when: time.Second}, seen) || flips() != 1 {
		t.Fatalf("own commit not traced: %s", log.String())
	}
	if o.lastFlipAt != time.Second {
		t.Fatalf("last flip %s", o.lastFlipAt)
	}

	o.frame.busy(time.Now())
	if !o.completed(flipEvent{crtc: tCrtc, user: 12345}, seen) || flips() != 1 {
		t.Fatalf("EBUSY wait end traced: %s", log.String())
	}
}

// A modeset starts a new flip interval: the time the output was inactive
// is not reported as a frame interval.
func TestModesetResetsFlipTrace(t *testing.T) {
	o, k, _ := testOutput(t)
	o.lastFlipAt = time.Second
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if o.lastFlipAt != 0 {
		t.Fatalf("after modeset: last flip %s", o.lastFlipAt)
	}
}
