package screencast

import (
	"errors"
	"slices"
	"testing"
	"time"

	"bnema.dev/purego-pipewire/pipewire"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/mock"
)

var (
	sdrOffer = []pipewire.VideoFormat{pipewire.VideoFormatBGRx, pipewire.VideoFormatBGRA}
	hdrOffer = []pipewire.VideoFormat{pipewire.VideoFormatXBGR210LE, pipewire.VideoFormatBGRx, pipewire.VideoFormatBGRA}
)

// open opens the cast at the given HDR state and returns the source config.
func (p *producer) open(hdr bool) pipewire.VideoConfig {
	p.t.Helper()
	p.s.pump(p.st, p.now)
	p.cmd()
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32, HDR: hdr})
	var cfg pipewire.VideoConfig
	p.core.EXPECT().NewVideoSource(mock.Anything, mock.Anything).RunAndReturn(
		func(c pipewire.VideoConfig, _ pipewire.VideoHandler) (videoSource, error) {
			cfg = c
			cfg.Formats = slices.Clone(c.Formats)
			return p.src, nil
		}).Once()
	p.s.pump(p.st, p.now)
	return cfg
}

func TestProducerOffersHDRFormatsOnlyForHDRTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hdr   bool
		want  []pipewire.VideoFormat
		color pipewire.VideoColorimetry
	}{
		{"sdr", false, sdrOffer, pipewire.VideoColorimetry{}},
		{"hdr", true, hdrOffer, pipewire.HDR10Colorimetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProducer(t)
			cfg := p.open(tc.hdr)
			if !slices.Equal(cfg.Formats, tc.want) || cfg.Color != tc.color {
				t.Fatalf("offer %v %+v", cfg.Formats, cfg.Color)
			}
		})
	}
}

// An HDR toggle replaces the offer on the live node, never reopens it, and
// waits for the frame in flight; the session stays.
func TestProducerHDRToggleSetsFormats(t *testing.T) {
	p := newProducer(t)
	p.open(false)
	p.st.h2.StateChanged(pipewire.StreamStatePaused, 42, "")
	p.st.h2.FormatChanged(pipewire.VideoFormatBGRx, 64, 32, 256)
	p.st.h2.StateChanged(pipewire.StreamStateStreaming, 42, "")
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != domain.CastFrame || c.Format != domain.CastXRGB8888 {
		t.Fatalf("frame %+v", c)
	}
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32, HDR: true})
	p.s.pump(p.st, p.now) // the frame is in flight: no SetFormats yet
	p.src.EXPECT().SetFormats(hdrOffer, pipewire.HDR10Colorimetry).Return(nil).Once()
	p.src.EXPECT().Queue(p.buf, mock.Anything, mock.Anything, false).Return().Once()
	p.src.EXPECT().Trigger().Return().Once()
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastFrameDone, ID: 1, Buf: 3})
	p.s.pump(p.st, p.now)
	if p.st.closing || p.st.sess.dying {
		t.Fatal("toggle ended the session")
	}
	// The consumer picks the PQ format: frames ask for XB30.
	p.st.h2.FormatChanged(pipewire.VideoFormatXBGR210LE, 64, 32, 256)
	p.buf.Format = pipewire.VideoFormatXBGR210LE
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.now = p.now.Add(time.Second)
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != domain.CastFrame || c.Format != domain.CastXBGR2101010 {
		t.Fatalf("PQ frame %+v", c)
	}
	// Back to SDR.
	p.src.EXPECT().Release(p.buf).Return().Once()
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastFrameDone, ID: 1, Buf: 3, Err: errors.New("frame failed")})
	p.src.EXPECT().SetFormats(sdrOffer, pipewire.VideoColorimetry{}).Return(nil).Once()
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32})
	p.src.EXPECT().Dequeue().Return(nil).Maybe()
	p.s.pump(p.st, p.now)
	// Unchanged state: nothing to renegotiate.
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32})
	p.s.pump(p.st, p.now)
}

func TestShmFormatRoundTrip(t *testing.T) {
	for _, f := range []uint32{domain.CastXRGB8888, domain.CastARGB8888, domain.CastXBGR2101010} {
		if got := shmFormat(videoFormat(f)); got != f {
			t.Errorf("%#x -> %#x", f, got)
		}
	}
}

// A source that still holds a lent buffer refuses SetFormats and Resize
// with ErrVideoBorrowed: the stream retries later instead of ending.
func TestProducerRetriesBorrowedRenegotiation(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   domain.CastEvent
	}{
		{"formats", domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32, HDR: true}},
		{"resize", domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 32, Height: 16}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProducer(t)
			p.open(false)
			p.st.h2.StateChanged(pipewire.StreamStatePaused, 42, "")
			p.st.h2.FormatChanged(pipewire.VideoFormatBGRx, 64, 32, 256)
			p.st.h2.StateChanged(pipewire.StreamStateStreaming, 42, "")
			p.s.onCastEvent(&tc.ev)
			if tc.name == "formats" {
				p.src.EXPECT().SetFormats(hdrOffer, pipewire.HDR10Colorimetry).Return(pipewire.ErrVideoBorrowed).Once()
			} else {
				p.src.EXPECT().Resize(32, 16).Return(pipewire.ErrVideoBorrowed).Once()
			}
			p.s.pump(p.st, p.now)
			if p.st.closing || p.st.sess.dying {
				t.Fatal("borrowed buffer ended the session")
			}
			if p.s.waitFor == 0 {
				t.Fatal("no retry scheduled")
			}
			// No frame is requested while the change waits.
			p.none()
			if tc.name == "formats" {
				p.src.EXPECT().SetFormats(hdrOffer, pipewire.HDR10Colorimetry).Return(nil).Once()
			} else {
				p.src.EXPECT().Resize(32, 16).Return(nil).Once()
			}
			p.now = p.now.Add(time.Second)
			p.src.EXPECT().Dequeue().Return(nil).Maybe()
			p.s.pump(p.st, p.now)
			if p.st.needFormats || p.st.needResize {
				t.Fatal("change not applied on retry")
			}
		})
	}
}
