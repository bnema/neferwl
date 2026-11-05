package screencast

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"bnema.dev/purego-pipewire/pipewire"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
)

// tempCast is a cast buffer of n planes in one temporary file of size bytes.
func tempCast(t *testing.T, format uint32, w, h, n int, size int64) (domain.DMABuf, *os.File) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cast")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	buf := domain.DMABuf{Width: w, Height: h, Format: format, Modifier: 7}
	buf.Planes = append(buf.Planes, domain.DMABufPlane{File: f, Stride: uint32(w * 4)})
	for i := 1; i < n; i++ {
		dup, err := os.Open(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		buf.Planes = append(buf.Planes, domain.DMABufPlane{File: dup, Offset: uint32(i * 1024), Stride: 64})
	}
	return buf, f
}

func closed(f *os.File) bool { _, err := f.Stat(); return err != nil }

func TestDMABufFormatsGroupsByCastFormat(t *testing.T) {
	got := dmabufFormats([]domain.DMABufFormat{
		{Format: domain.CastXRGB8888, Modifier: 5}, {Format: domain.CastXBGR2101010, Modifier: 0},
		{Format: domain.CastXRGB8888, Modifier: 0}, {Format: domain.CastXRGB8888, Modifier: 5}, {Format: 0x3231564e},
	})
	want := []pipewire.VideoDMABufFormat{
		{Format: pipewire.VideoFormatXBGR210LE, Modifiers: []uint64{0}},
		{Format: pipewire.VideoFormatBGRx, Modifiers: []uint64{5, 0}},
	}
	if len(got) != len(want) {
		t.Fatalf("formats %+v", got)
	}
	for i := range want {
		if got[i].Format != want[i].Format || !slices.Equal(got[i].Modifiers, want[i].Modifiers) {
			t.Fatalf("formats %+v", got)
		}
	}
}

// Fixate takes the first candidate the GPU lists and exports, and reports
// its plane count; nothing allocated for the trial is kept.
func TestCastAllocatorFixate(t *testing.T) {
	gpu := portsmocks.NewMockCastBufferAllocator(t)
	gpu.EXPECT().ReleaseCast(mock.Anything).Maybe()
	gpu.EXPECT().CastFormats().Return([]domain.DMABufFormat{{Format: domain.CastXRGB8888, Modifier: 7}, {Format: domain.CastXRGB8888, Modifier: 9}})
	trial, f := tempCast(t, domain.CastXRGB8888, 64, 32, 2, 1<<16)
	gpu.EXPECT().AllocCast(domain.CastXRGB8888, 64, 32, uint64(9)).Return(domain.DMABuf{}, errors.New("no memory")).Once()
	gpu.EXPECT().AllocCast(domain.CastXRGB8888, 64, 32, uint64(7)).Return(trial, nil).Once()
	a := &castAllocator{dev: gpu}
	m, n, err := a.Fixate(pipewire.VideoFormatBGRx, 64, 32, []uint64{3, 9, 7})
	if err != nil || m != 7 || n != 2 {
		t.Fatalf("fixate %#x %d %v", m, n, err)
	}
	if !closed(f) {
		t.Fatal("trial buffer kept")
	}
	if _, _, err := a.Fixate(pipewire.VideoFormatBGRx, 64, 32, []uint64{3}); err == nil {
		t.Fatal("unlisted modifier fixated")
	}
}

// Alloc describes the planes in the object's size; Free closes them. A
// failed Alloc keeps nothing.
func TestCastAllocatorAllocFree(t *testing.T) {
	gpu := portsmocks.NewMockCastBufferAllocator(t)
	buf, f := tempCast(t, domain.CastXRGB8888, 64, 32, 2, 1<<16)
	gpu.EXPECT().AllocCast(domain.CastXRGB8888, 64, 32, uint64(7)).Return(buf, nil).Once()
	a := &castAllocator{dev: gpu}
	var out pipewire.VideoPlanes
	if err := a.Alloc(4, pipewire.VideoFormatBGRx, 64, 32, 7, &out); err != nil {
		t.Fatal(err)
	}
	if out.N != 2 || out.Size != 1<<16 || out.P[0].FD != int(f.Fd()) || out.P[0].Stride != 256 || out.P[1].Offset != 1024 || out.P[1].Size != 1<<16-1024 {
		t.Fatalf("planes %+v", out)
	}
	if got := a.buffer(&pipewire.VideoBuffer{Index: 4, DMABuf: true}); got == nil || got.Planes[0].File != f {
		t.Fatalf("buffer %+v", got)
	}
	if a.buffer(&pipewire.VideoBuffer{Index: 4}) != nil {
		t.Fatal("memfd buffer has a dmabuf")
	}
	gpu.EXPECT().ReleaseCast(buf).Run(func(b domain.DMABuf) {
		if closed(b.Planes[0].File) {
			t.Fatal("released after close")
		}
	}).Once()
	a.Free(4)
	if !closed(f) || a.buffer(&pipewire.VideoBuffer{Index: 4, DMABuf: true}) != nil {
		t.Fatal("buffer not freed")
	}

	// A plane past the object: the allocation is refused and closed.
	bad, g := tempCast(t, domain.CastXRGB8888, 64, 32, 2, 512)
	gpu.EXPECT().ReleaseCast(bad).Once()
	gpu.EXPECT().AllocCast(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(bad, nil).Once()
	if err := a.Alloc(5, pipewire.VideoFormatBGRx, 64, 32, 7, &out); err == nil {
		t.Fatal("bad planes accepted")
	}
	if !closed(g) || a.buffer(&pipewire.VideoBuffer{Index: 5, DMABuf: true}) != nil {
		t.Fatal("failed allocation kept")
	}
	gpu.EXPECT().AllocCast(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(domain.DMABuf{}, errors.New("oom")).Once()
	if err := a.Alloc(5, pipewire.VideoFormatBGRx, 64, 32, 7, &out); err == nil {
		t.Fatal("allocation error swallowed")
	}
	if err := a.Alloc(maxCastBuffers, pipewire.VideoFormatBGRx, 64, 32, 7, &out); err == nil {
		t.Fatal("index out of range accepted")
	}
}

// A consumer that cannot take DMA-BUF ends the cast with a clear reason
// rather than getting CPU frames.
func TestDMABufFailureEndsCast(t *testing.T) {
	out := &logBuf{}
	p := newProducer(t)
	p.s.log = testLog(out)
	p.streaming()
	p.st.h2.dmabufFailed(errors.New("consumer negotiated without modifier"))
	p.src.EXPECT().Destroy().Return().Once()
	p.s.pump(p.st, p.now)
	if !p.s.sessions[0].dying {
		t.Fatal("cast not ended")
	}
	p.none()
	p.s.pump(p.st, p.now)
	if c := p.cmd(); c.Kind != domain.CastClose {
		t.Fatalf("%+v", c)
	}
	if s := out.String(); !strings.Contains(s, `"reason":"dmabuf"`) || !strings.Contains(s, "consumer negotiated without modifier") || !strings.Contains(s, `"component":"screencast"`) {
		t.Fatalf("log %s", s)
	}
}

// Without DMA-BUF cast formats no PipeWire node is created at all.
func TestNoCastFormatsNoSource(t *testing.T) {
	p := newProducer(t)
	p.gpu.ExpectedCalls = nil
	p.gpu.EXPECT().CastFormats().Return(nil)
	p.s.pump(p.st, p.now)
	p.cmd()
	p.s.onCastEvent(&domain.CastEvent{Kind: domain.CastSize, ID: 1, Width: 64, Height: 32})
	p.s.pump(p.st, p.now) // NewVideoSource is not expected
	if !p.s.sessions[0].dying {
		t.Fatal("cast kept without DMA-BUF")
	}
}

// A buffer without a dmabuf (never allocated) is not lent.
func TestMemfdBufferNotLent(t *testing.T) {
	p := newProducer(t)
	p.streaming()
	closePlanes(&p.st.alloc.bufs[3])
	p.src.EXPECT().Dequeue().Return(p.buf).Once()
	p.src.EXPECT().Release(p.buf).Return().Once()
	p.s.pump(p.st, p.now)
	p.none()
}
