package capture

import (
	"context"
	"errors"
	"fmt"
	"image"
	"maps"
	"math"
	"os"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Offscreen capture: a session of a workspace that is not on screen is drawn
// by a dedicated child renderer from Scene.CaptureScene, only for clean
// requests and never shown. The child is a separate renderer instance: it has
// its own Vulkan device, so its fences are not ordered against the display's.
// It is created on the first clean request of the session and closed when the
// scene stops carrying CaptureScene (once idle), when the session or the frame
// size changes, and with the output.

const (
	// MaxOffscreenDim bounds either side of the child image, in pixels.
	MaxOffscreenDim = 16384
	// MaxOffscreenBytes bounds one child image (w*h*4).
	MaxOffscreenBytes = 256 << 20
	// maxChildFences bounds child renders whose GPU reads are not finished.
	// It equals the renderer's frame slots (two): a third Render would make
	// the owner wait for the GPU, so it is refused before it is recorded.
	maxChildFences = 2
)

var (
	// ErrOffscreenUnavailable fails a clean request whose source is a hidden
	// workspace that this output cannot draw (no child renderer factory).
	ErrOffscreenUnavailable = errors.New("workspace capture source is unavailable")
	// ErrOffscreenGeometry fails a request the child frame cannot serve.
	ErrOffscreenGeometry = errors.New("workspace capture geometry is unsupported")
	// ErrOffscreenBusy fails a request while the child is still busy with
	// another session or size, or has too many renders in flight.
	ErrOffscreenBusy = errors.New("workspace capture is busy")
)

// fenceSlot is one child render whose GPU reads may not be finished: its sync
// file and the read ceiling it implies. ceil holds, for each window the render
// drew, the content Seq the renderer was given. The map is reused for the
// output's lifetime (cleared and refilled), so a steady state allocates
// nothing.
type fenceSlot struct {
	fd   *os.File
	ceil map[ports.WindowID]uint64
}

// release closes the fence and forgets the ceiling, keeping the map.
func (s *fenceSlot) release() {
	if s.fd != nil {
		s.fd.Close()
		s.fd = nil
	}
	clear(s.ceil)
}

// offscreen is owned by the goroutine of its Pipeline.
type offscreen struct {
	factory func(w, h int) (ports.Renderer, error)
	r       ports.Renderer
	p       *Pipeline // leases of r
	w, h    int
	session uint64
	// slots[:nf] are the unfinished child renders, oldest first.
	slots [maxChildFences]fenceSlot
	nf    int
	clean cleaner
	// capped is the scratch map CapHiddenSeen returns.
	capped map[ports.WindowID]uint64
}

// EnableOffscreen lets the pipeline serve clean requests of a hidden
// workspace with renderers made by factory. Without it they fail closed
// with ErrOffscreenUnavailable.
func (p *Pipeline) EnableOffscreen(factory func(w, h int) (ports.Renderer, error)) {
	p.off = &offscreen{factory: factory}
}

// HiddenCompleted wakes the owner when a child capture finished; nil (blocks
// forever in a select) while there is no child.
func (p *Pipeline) HiddenCompleted() <-chan *batch {
	if p == nil || p.off == nil || p.off.p == nil {
		return nil
	}
	return p.off.p.completed
}

// RecycleHidden returns a completed child lease.
func (p *Pipeline) RecycleHidden(b *batch) {
	if p != nil && p.off != nil && p.off.p != nil {
		p.off.p.Recycle(b, p.off.r)
	}
}

// HiddenReading reports whether the child may still read client buffers. The
// display's fences do not cover it.
func (p *Pipeline) HiddenReading() bool {
	if p == nil || p.off == nil {
		return false
	}
	p.off.reap()
	return p.off.nf > 0
}

// CapHiddenSeen limits seen, the content Seq per window an output has read,
// to what the unfinished child renders were given: a client buffer newer than
// that may not be released yet on their account. Only windows a render drew
// are capped, each by the lowest ceiling among the unfinished renders that
// drew it (a window the render drew without content is capped at 0); every
// other window is untouched, so the display's own reads are never held back.
// It returns seen itself, and false, when nothing is capped. Otherwise the
// result is a scratch map owned by the pipeline, valid until the next call;
// copy it to keep it.
func (p *Pipeline) CapHiddenSeen(seen map[ports.WindowID]uint64) (map[ports.WindowID]uint64, bool) {
	if p == nil || p.off == nil {
		return seen, false
	}
	o := p.off
	o.reap()
	if o.nf == 0 {
		return seen, false
	}
	hit := false
	for i := range o.nf {
		for id, c := range o.slots[i].ceil {
			if seen[id] > c {
				hit = true
			}
		}
	}
	if !hit {
		return seen, false
	}
	if o.capped == nil {
		o.capped = make(map[ports.WindowID]uint64, len(seen))
	}
	clear(o.capped)
	maps.Copy(o.capped, seen)
	for i := range o.nf {
		for id, c := range o.slots[i].ceil {
			if v, ok := o.capped[id]; ok && v > c {
				o.capped[id] = c
			}
		}
	}
	return o.capped, true
}

// WaitHidden blocks until the child finished reading (headless outputs, which
// have no screen pacing frames anyway). Whatever happens, every fence is
// closed on return: after a failure or a cancelled ctx the rest is not waited
// for (the renderer's Close waits for the device before it frees anything).
func (p *Pipeline) WaitHidden(ctx context.Context) error {
	if p == nil || p.off == nil {
		return nil
	}
	o := p.off
	var first error
	for i := range o.nf {
		if first == nil {
			first = syncfile.Wait(ctx, o.slots[i].fd)
		}
		o.slots[i].release()
	}
	o.nf = 0
	return first
}

// Retire closes the child once the scene no longer carries CaptureScene and
// nothing of it is in flight. Call it when a frame is prepared, when a child
// lease is recycled and on the idle housekeeping tick.
func (p *Pipeline) Retire(s ports.Scene) {
	if p != nil && p.off != nil && s.CaptureScene == nil && p.off.r != nil && !p.off.busy() {
		p.off.close()
	}
}

// reap forgets the renders whose fence signalled, oldest first kept in order.
func (o *offscreen) reap() {
	k := 0
	for i := range o.nf {
		if pollReady(o.slots[i].fd) {
			o.slots[i].release()
			continue
		}
		if k != i {
			o.slots[k], o.slots[i] = o.slots[i], o.slots[k]
		}
		k++
	}
	o.nf = k
}

// pollReady reports whether a sync file signalled, without waiting.
func pollReady(f *os.File) bool {
	fd := int32(f.Fd())
	if fd < 0 {
		return true // closed: poll would skip it, and nothing is left to wait for
	}
	pfd := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
	n, err := unix.Poll(pfd, 0)
	return pollResult(n, err, pfd[0].Revents)
}

// pollResult decides from a zero-timeout poll. Only a readable fence is
// finished (an invalid descriptor has nothing left to wait for). An
// interrupted poll, a timeout, and an error status (POLLERR: the GPU failed
// the work) are not finished: the fence keeps holding what it protects until
// the device is closed.
func pollResult(n int, err error, revents int16) bool {
	switch {
	case errors.Is(err, unix.EBADF):
		return true
	case err != nil || n <= 0:
		return false
	}
	return revents&(unix.POLLIN|unix.POLLNVAL) != 0 && revents&unix.POLLERR == 0
}

func (o *offscreen) busy() bool {
	o.reap()
	if o.nf > 0 {
		return true
	}
	if o.p != nil {
		o.p.reclaim(o.r)
		for i := range o.p.batches {
			if o.p.batches[i].frame != nil {
				return true
			}
		}
	}
	return false
}

func (o *offscreen) close() {
	if o.p != nil {
		o.p.Close(o.r)
		o.p = nil
	}
	if o.r != nil {
		o.r.Close()
		o.r = nil
	}
	for i := range o.slots {
		o.slots[i].release()
	}
	o.nf, o.w, o.h, o.session = 0, 0, 0, 0
}

// childSize is the physical child image: the workspace frame at the scene
// scale, rounded up.
func childSize(cs *ports.Scene) (w, h int, err error) {
	scale := cs.Scale
	if scale <= 0 {
		scale = 1
	}
	fw, fh := math.Ceil(float64(cs.OutputWidth)*scale), math.Ceil(float64(cs.OutputHeight)*scale)
	if !(fw >= 1 && fh >= 1 && fw <= MaxOffscreenDim && fh <= MaxOffscreenDim) || fw*fh*4 > MaxOffscreenBytes {
		return 0, 0, fmt.Errorf("%w: frame %.0fx%.0f", ErrOffscreenGeometry, fw, fh)
	}
	return int(fw), int(fh), nil
}

// track records the ceiling of a child render on the next free slot: every
// window and layer the render drew, with the content Seq it was given.
func (o *offscreen) track(done *os.File, drawn ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent) {
	slot := &o.slots[o.nf]
	if slot.ceil == nil {
		slot.ceil = make(map[ports.WindowID]uint64, len(drawn.Windows)+len(drawn.Layers))
	}
	clear(slot.ceil)
	for _, w := range drawn.Windows {
		slot.ceil[w.ID] = surfaces[w.ID].Seq
	}
	for _, l := range drawn.Layers {
		slot.ceil[l.ID] = surfaces[l.ID].Seq
	}
	slot.fd = done
	o.nf++
}

// SubmitHidden serves the clean requests of s from the child renderer, which
// draws s.CaptureScene without the session's excluded surfaces. It consumes
// every request: answered by the worker, or failed here (the display is not
// stopped by a child failure). Request regions are physical output pixels and
// must cover the whole child frame; they are rebased to its origin. The target
// must be the whole workspace frame too. Anything smaller needs the viewport
// origin in output coordinates, which the scene does not carry, and fails with
// ErrOffscreenGeometry.
func (p *Pipeline) SubmitHidden(s ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, reqs []ports.CaptureRequest) {
	fail := func(err error, qs []ports.CaptureRequest) {
		for _, q := range qs {
			Fail(p.ctx, q, err, p.replies)
		}
	}
	o := p.off
	switch {
	case len(reqs) == 0:
		return
	case s.Off:
		fail(errors.New("output off"), reqs)
		return
	case s.CaptureScene == nil || s.Capture == nil:
		fail(ErrOffscreenUnavailable, reqs)
		return
	case o == nil || o.factory == nil:
		fail(ErrOffscreenUnavailable, reqs)
		return
	case p.ctx.Err() != nil:
		fail(p.ctx.Err(), reqs)
		return
	}
	cs := s.CaptureScene
	w, h, err := childSize(cs)
	if err == nil {
		t := s.Capture.TargetRect
		if t.W != cs.OutputWidth || t.H != cs.OutputHeight {
			err = fmt.Errorf("%w: target %dx%d is not the frame %dx%d", ErrOffscreenGeometry, t.W, t.H, cs.OutputWidth, cs.OutputHeight)
		}
	}
	if err != nil {
		fail(err, reqs)
		return
	}
	if o.r != nil && (o.session != s.Capture.Session || o.w != w || o.h != h) {
		if o.busy() {
			fail(ErrOffscreenBusy, reqs)
			return
		}
		o.close()
	}
	o.reap()
	if o.nf == len(o.slots) {
		// Refused before Render: a third frame would make the owner wait.
		fail(ErrOffscreenBusy, reqs)
		return
	}
	if o.r == nil {
		r, err := o.factory(w, h)
		if err != nil {
			fail(fmt.Errorf("%w: %v", ErrOffscreenUnavailable, err), reqs)
			return
		}
		o.r, o.p, o.w, o.h, o.session = r, NewPipeline(p.ctx, p.replies), w, h, s.Capture.Session
	}
	// Request regions are physical pixels of the output; TargetRect is
	// relative to the workspace viewport, so it cannot place them. The child
	// image is the whole target (checked above): a request for all of it has
	// the child's size and starts at the child's origin. A smaller region
	// would need the viewport origin in output coordinates, which the scene
	// does not carry; guessing it would return the wrong crop, so it fails.
	k := 0
	for _, q := range reqs {
		if q.Region.Dx() != w || q.Region.Dy() != h {
			Fail(p.ctx, q, fmt.Errorf("%w: region %v is not the whole %dx%d workspace frame", ErrOffscreenGeometry, q.Region, w, h), p.replies)
			continue
		}
		q.Region = image.Rect(0, 0, w, h)
		reqs[k] = q
		k++
	}
	clear(reqs[k:])
	reqs = reqs[:k]
	if k == 0 {
		return
	}
	// The child never shows the session's own surfaces or border.
	child := *cs
	child.Capture = &ports.SceneCapture{Excluded: s.Capture.Excluded}
	child = o.clean.scene(child)
	done, err := o.r.Render(child, surfaces)
	if err != nil {
		fail(fmt.Errorf("render workspace: %w", err), reqs)
		return
	}
	if done != nil {
		o.track(done, child, surfaces)
	}
	o.p.Submit(o.r, reqs)
}
