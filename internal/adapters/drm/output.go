package drm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/adapters/clock"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Output is one KMS connector on a CRTC, driven by atomic commits (ADR
// 015). One goroutine runs it (Run) and closes it. Every commit that
// changes the screen asks for an event and leaves the output pending until
// it arrives: the next commit waits for it. The kernel may still refuse
// it with EBUSY (it retries soon), and an event that never comes is
// bounded (stuckTimeout, then a modeset).
type Output struct {
	k           kms
	flipped     <-chan flipEvent // commit events of this CRTC, from Card.ReadEvents
	crtc        uint32
	conn        connector
	mode        modeInfo
	saved       modeCrtc
	monitor     Monitor
	hdr         hdrCapability
	hdrProps    connectorHDRProps
	hdrSettings HDRSettings
	// hdrFailed disables retries until this Output is replaced; hdrOn is
	// the currently selected signal encoding, including on VT resume.
	hdrOn, hdrFailed bool
	hdrBlob          uint32
	hdrBlobData      hdrOutputMetadata
	log              zerowrap.Logger
	// clock paces the periodic stats and renderer trim (nil: system).
	clock ports.Clock
	// Properties: CRTC and connector property IDs by name.
	crtcProps map[string]uint32
	connCrtc  uint32 // the connector's CRTC_ID property
	modeBlob  uint32
	// primary shows frames; cursor is the cursor plane (nil: none);
	// stray are other planes that can show on this CRTC, turned off at
	// every modeset when a previous master left them on it.
	primary *plane
	cursor  *Cursor
	stray   []*plane
	// overlay shows one window's buffer above the composed frame
	// (overlay.go); overlayOn is the buffer on it in the pending or last
	// frame (0: off), overlayReason why it is off.
	overlay       *plane
	overlayOn     uint64
	overlayReason string
	// fbs are the two renderer images frames alternate between, back the
	// one the next frame draws into (ADR 014: zero copy).
	fbs   [2]uint32
	back  int
	frame frameLifecycle // pending commit, serials, deadlines and timer
	flips int
	// readFences are fences of frames rendered but not committed: the
	// GPU may still read client buffers until they signal, so what was
	// seen is reported only then (or after a later frame flipped).
	readFences []*os.File
	// Direct scanout: client framebuffers by DMABuf ID and the buffer on
	// screen and queued (0: the composed image).
	scanout       bool
	clientFBs     map[uint64]*clientFB
	shown, queued uint64
	reason        string // why the last frame was composed ("" = scanout)
	unsent        []ports.OutputPresented
	// Last immutable snapshot sent in a report. Never mutate it: wayland may
	// still be reading a prior report on another goroutine.
	seenSnapshot        map[ports.WindowID]uint64
	showsSnapshot       map[ports.WindowID]uint64
	directShowsSnapshot map[ports.WindowID]uint64
	showsScratch        map[ports.WindowID]uint64
	// Tearing (ADR 006): async commits of scanned-out buffers that ask for
	// them, when the card has DRM_CAP_ATOMIC_ASYNC_PAGE_FLIP. asyncFence
	// is set when an async commit may carry IN_FENCE_FD (probed once).
	tearing     bool
	asyncFence  bool
	asyncProbed bool
	async       bool
	// VRR: the CRTC's VRR_ENABLED property (0: none), on while a buffer is
	// scanned out or a fullscreen window covers the output, set inside
	// frame commits. vrrGame is that state for the last frame.
	vrrProp       uint32
	vrrOn         bool
	vrrGame       bool
	composedSince time.Time
	// lastFrame is when the last frame was committed. cursorHeld: a cursor
	// move waits for the next frame (see cursorWaits).
	lastFrame  time.Time
	cursorHeld bool
	// wantOff is the latest Scene.Off: a client turned the display off.
	// off: the CRTC is inactive for it. Every modeset (resume, recovery)
	// follows wantOff, so a display turned off never lights up.
	wantOff, off bool
	// kind is how the images were made.
	kind imageKind
	// formats receives the direct scanout formats after each modeset;
	// sampled and device are what they are built from (Want).
	formats   chan<- ports.OutputFormats
	sampled   []ports.DMABufFormat
	device    uint64
	ready     chan error // first successful modeset, or a startup failure
	readySent bool
}

// Ready reports the result of the first modeset exactly once.
func (o *Output) Ready() <-chan error { return o.ready }

// dupFences duplicates the non-nil fences.
func dupFences(fs ...*os.File) []*os.File {
	var out []*os.File
	for _, f := range fs {
		if d := dupFence(f); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// signalled reports whether every fence signalled (readable); an fd that
// cannot be polled counts as signalled.
func signalled(fs []*os.File) bool {
	if len(fs) == 0 {
		return true
	}
	pfds := make([]unix.PollFd, len(fs))
	for i, f := range fs {
		pfds[i] = unix.PollFd{Fd: int32(f.Fd()), Events: unix.POLLIN}
	}
	if _, err := unix.Poll(pfds, 0); err != nil {
		return true
	}
	for _, p := range pfds {
		if p.Revents == 0 {
			return false
		}
	}
	return true
}

// holdRead keeps the fences of a frame that was not committed.
func (o *Output) holdRead(fs ...*os.File) {
	o.readFences = append(o.readFences, dupFences(fs...)...)
}

// readDone reports whether no uncommitted frame may still read client
// buffers, closing the fences that signalled.
func (o *Output) readDone() bool {
	kept := o.readFences[:0]
	for _, f := range o.readFences {
		if signalled([]*os.File{f}) {
			f.Close()
			continue
		}
		kept = append(kept, f)
	}
	clear(o.readFences[len(kept):])
	o.readFences = kept
	return len(kept) == 0
}

// dropRead closes the fences of uncommitted frames.
func (o *Output) dropRead() {
	for _, f := range o.readFences {
		f.Close()
	}
	o.readFences = nil
}

// expire applies a lifecycle deadline and reports whether KMS needs a modeset.
// Fence polling and logging stay at the output boundary, not in the value
// transition; the lifecycle owns the decision and closes pending fences.
func (o *Output) expire() bool {
	now := time.Now()
	serial, frame, age, busy, fences := o.frame.deadlineInfo(now)
	ready := serial == 0 || !o.frame.deadlineReached(now) || signalled(fences)
	switch o.frame.timeout(now, ready) {
	case timeoutFence:
		o.log.Warn().Str("connector", o.conn.name).Uint64("serial", serial).Dur("age", age).Msg("frame waits for GPU fence")
	case timeoutBusy:
		o.log.Warn().Str("connector", o.conn.name).Str("kind", "busy").Dur("age", busy).Msg("commits refused as busy; modeset")
		return true
	case timeoutMissing:
		kind := "state"
		if frame {
			kind = "frame"
		}
		o.log.Warn().Str("connector", o.conn.name).Uint64("serial", serial).Str("kind", kind).Dur("age", age).Msg("commit event missing; modeset")
		return true
	}
	return false
}

// CursorLoader returns the image of a cursor at an output scale, at most
// limit pixels on a side; an empty image hides the cursor.
type CursorLoader func(c ports.CursorChange, scale float64, limit int) (ports.CursorImage, error)

// newOutput reads the CRTC's planes and properties for a connector on crtc.
func newOutput(card *Card, c connector, mode modeInfo, crtc uint32) (*Output, error) {
	log := card.log
	pipe := slices.Index(card.crtcs, crtc)
	o := &Output{k: card.k, flipped: card.flips[crtc], frame: frameLifecycle{serials: &card.serials}, formats: card.formats, sampled: card.want.Sampled, device: card.want.Device, crtc: crtc, conn: c, mode: mode, log: log, monitor: readMonitor(card.path, c.name), scanout: !card.want.NoScanout, clientFBs: map[uint64]*clientFB{}, reason: "start", ready: make(chan error, 1)}
	var err error
	if o.saved, err = getCrtc(card.fd, crtc); err != nil {
		log.Warn().Err(err).Uint32("crtc", crtc).Msg("save crtc; it will not be restored on exit")
	}
	if err := o.readProps(card.fd, pipe, card.taken, cursorSize(card.fd)); err != nil {
		return nil, err
	}
	o.hdrSettings = normalizedHDRSettings(card.want.HDR[c.name])
	o.hdr = detectHDR(o.monitor, o.hdrProps)
	log.Info().Str("connector", c.name).Bool("hdr_capable", o.hdr.Capable).Str("reason", o.hdr.Reason).Float64("max_luminance", o.hdr.MaxLuminance).Float64("max_frame_average", o.hdr.MaxFrameAverage).Float64("min_luminance", o.hdr.MinLuminance).Msg("HDR capability")
	if o.hdrSettings.Enabled && !o.hdr.Capable {
		log.Warn().Str("connector", c.name).Str("reason", o.hdr.Reason).Msg("HDR requested but unavailable")
	}
	o.tearing = card.async && !card.want.NoTearing
	if !card.want.NoVRR {
		o.vrrProp = vrrProperty(o.k, c.id, crtc)
	}
	log.Info().Str("card", card.path).Str("connector", c.name).Str("mode", mode.String()).Str("make", o.monitor.Make).Str("model", o.monitor.Model).Uint32("crtc", crtc).Uint32("primary", o.primary.id).Bool("cursor", o.cursor != nil).Bool("tearing", o.tearing).Bool("vrr", o.vrrProp != 0).Msg("output")
	return o, nil
}

// readProps finds the output's planes and property IDs. taken are planes
// other outputs drive.
func (o *Output) readProps(fd, pipe int, taken map[uint32]bool, cursorSide int) error {
	cp, err := o.k.objProps(o.crtc, objCrtc)
	if err != nil {
		return fmt.Errorf("crtc properties: %w", err)
	}
	o.crtcProps = map[string]uint32{}
	for name, v := range cp {
		o.crtcProps[name] = uint32(v[0])
	}
	np, err := o.k.objProps(o.conn.id, objConnector)
	if err != nil {
		return fmt.Errorf("connector properties: %w", err)
	}
	o.connCrtc = uint32(np["CRTC_ID"][0])
	// The connector's property metadata is needed for enum/range capability.
	o.hdrProps = readConnectorHDRProps(fd, np)
	if o.crtcProps["MODE_ID"] == 0 || o.crtcProps["ACTIVE"] == 0 || o.connCrtc == 0 {
		return errors.New("crtc or connector lacks atomic properties")
	}
	planes, err := readPlanes(o.k, pipe)
	if err != nil {
		return fmt.Errorf("planes: %w", err)
	}
	pick := func(typ uint32) *plane {
		var best *plane
		for _, p := range planes {
			if p.typ != typ || taken[p.id] {
				continue
			}
			// Prefer the plane already on this CRTC.
			if best == nil || p.crtc == o.crtc && best.crtc != o.crtc {
				best = p
			}
		}
		return best
	}
	if o.primary = pick(planePrimary); o.primary == nil {
		return errors.New("no free primary plane")
	}
	if p := pick(planeCursor); p != nil {
		o.cursor = newCursor(p, cursorSide)
	}
	for _, p := range planes {
		if p != o.primary && (o.cursor == nil || p != o.cursor.plane) && !taken[p.id] {
			o.stray = append(o.stray, p)
		}
	}
	o.pickOverlay()
	return nil
}

// owned are the planes this output drives.
func (o *Output) owned() []*plane {
	out := []*plane{o.primary}
	if o.cursor != nil {
		out = append(out, o.cursor.plane)
	}
	if o.overlay != nil {
		out = append(out, o.overlay)
	}
	return out
}

// cursorSize is the cursor plane side: the smaller of the driver's width
// and height caps, 64 when it does not say.
func cursorSize(fd int) int {
	size := 0
	for _, c := range []uint64{capCursorW, capCursorH} {
		v := getCap{capability: c}
		if ioctl(fd, ioctlGetCap, unsafe.Pointer(&v)) == nil && v.value >= 16 && v.value <= 512 {
			if size == 0 || int(v.value) < size {
				size = int(v.value)
			}
		}
	}
	if size == 0 {
		size = 64
	}
	return size
}

// Cursor is the hardware cursor, or nil when the CRTC has no cursor plane.
func (o *Output) Cursor() *Cursor { return o.cursor }

// Info describes the output for core and clients. Unknown EDID fields are empty.
func (o *Output) Info() ports.OutputInfo {
	known := func(v string) string {
		if v == "Unknown" {
			return ""
		}
		return v
	}
	return ports.OutputInfo{Name: o.conn.name, Make: known(o.monitor.Make), Model: known(o.monitor.Model), Serial: o.monitor.Serial, Width: o.Width(), Height: o.Height(), RefreshMilli: o.mode.refreshMilli(), PhysicalW: o.conn.mmW, PhysicalH: o.conn.mmH}
}

func (o *Output) Width() int  { return int(o.mode.HDisplay) }
func (o *Output) Height() int { return int(o.mode.VDisplay) }

// primaryProps shows fb full screen on the primary plane.
func (o *Output) primaryProps(req *atomicReq, fb uint32) {
	p := o.primary
	w, h := uint64(o.Width()), uint64(o.Height())
	req.set(p.id, p.prop("FB_ID"), uint64(fb))
	req.set(p.id, p.prop("CRTC_ID"), uint64(o.crtc))
	req.set(p.id, p.prop("SRC_X"), 0)
	req.set(p.id, p.prop("SRC_Y"), 0)
	req.set(p.id, p.prop("SRC_W"), w<<16)
	req.set(p.id, p.prop("SRC_H"), h<<16)
	req.set(p.id, p.prop("CRTC_X"), 0)
	req.set(p.id, p.prop("CRTC_Y"), 0)
	req.set(p.id, p.prop("CRTC_W"), w)
	req.set(p.id, p.prop("CRTC_H"), h)
}

// modeset shows the front image with the output's mode, turning off every
// other plane on the CRTC; needed at start and after every VT resume.
// It blocks until the kernel applied it.
func (o *Output) modeset() error {
	blob, err := o.modeBlobFor(o.mode)
	if err != nil {
		return err
	}
	if err := o.k.commit(o.modesetReq(blob, !o.wantOff), atomicAllowModes, 0); err != nil {
		_ = o.k.destroyBlob(blob)
		return fmt.Errorf("modeset: %w", err)
	}
	// Only now is nothing of ours pending or on screen: on failure the
	// previous buffers may still show.
	o.frame.resetAfterModeset()
	o.shown, o.queued = 0, 0
	if o.modeBlob != 0 {
		_ = o.k.destroyBlob(o.modeBlob)
	}
	o.modeBlob = blob
	o.vrrOn, o.vrrGame, o.overlayOn, o.off = false, false, 0, o.wantOff
	if o.cursor != nil {
		o.cursor.applied = cursorState{}
		o.cursor.screen, o.cursor.flying = 0, false
	}
	o.log.Info().Str("connector", o.conn.name).Bool("off", o.off).Msg("modeset")
	if !o.off {
		// An inactive CRTC tells nothing about the cursor plane.
		o.testCursor()
	}
	o.sendFormats()
	if o.ready != nil && !o.readySent {
		o.ready <- nil
		o.readySent = true
	}
	return nil
}

// powerOff turns the display off: the CRTC goes inactive and keeps its
// mode, as DPMS off. The commit blocks, so no event is pending after it;
// a modeset turns the display back on.
func (o *Output) powerOff() error {
	req := &atomicReq{}
	req.set(o.crtc, o.crtcProps["ACTIVE"], 0)
	req.set(o.crtc, o.vrrProp, 0)
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		return fmt.Errorf("power off: %w", err)
	}
	o.off, o.vrrOn, o.vrrGame = true, false, false
	o.log.Info().Str("connector", o.conn.name).Msg("power off")
	// An inactive output offers neither HDR nor direct scanout.
	o.sendFormats()
	return nil
}

// sendFormats reports the formats clients may allocate for direct
// scanout on this output (none when scanout is off). It never blocks:
// wayland keeps the latest per output, a dropped report is resent at the
// next modeset.
func (o *Output) sendFormats() {
	if o.formats == nil {
		return
	}
	f := ports.OutputFormats{Output: o.conn.name, Device: o.device}
	if o.hdrOn && !o.off {
		f.HDR = &ports.OutputHDR{MaxLuminance: o.hdr.MaxLuminance, MaxFrameAverage: o.hdr.MaxFrameAverage, MinLuminance: o.hdr.MinLuminance}
	}
	if o.scanout && !o.off {
		for _, format := range o.scanoutFormats(o.sampled) {
			if !isYUVFormat(format.Format) && isTenBit(format.Format) == o.hdrOn {
				f.Formats = append(f.Formats, format)
			}
		}
	}
	select {
	case o.formats <- f:
	default:
		o.log.Warn().Str("connector", o.conn.name).Msg("scanout formats not sent: wayland busy")
	}
}

// testModeset asks KMS whether it would take the modeset of the output
// images without applying it.
func (o *Output) testModeset() error {
	blob, err := o.modeBlobFor(o.mode)
	if err != nil {
		return err
	}
	defer func() { _ = o.k.destroyBlob(blob) }()
	// Tested active: an inactive CRTC would accept any image.
	if err := o.k.commit(o.modesetReq(blob, true), atomicTestOnly|atomicAllowModes, 0); err != nil {
		return fmt.Errorf("modeset test: %w", err)
	}
	return nil
}

func (o *Output) modeBlobFor(mode modeInfo) (uint32, error) {
	blob, err := o.k.createBlob(unsafe.Slice((*byte)(unsafe.Pointer(&mode)), unsafe.Sizeof(mode)))
	if err != nil {
		return 0, fmt.Errorf("mode blob: %w", err)
	}
	return blob, nil
}

// testCursor turns the hardware cursor off when KMS refuses its images on
// the cursor plane (e.g. a pitch the plane cannot scan out).
func (o *Output) testCursor() {
	c := o.cursor
	if c == nil || c.off || c.fbs[0] == 0 {
		return
	}
	req := &atomicReq{}
	c.props(req, o.crtc, cursorState{on: true, fb: c.fbs[0]})
	if err := o.k.commit(req, atomicTestOnly, 0); refused(err) {
		o.cursorOff(err)
	}
}

// cursorOff stops using the cursor plane for the rest of the output's life.
func (o *Output) cursorOff(err error) {
	o.cursor.off = true
	o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("cursor plane refused; hardware cursor off")
}

// modesetReq is the modeset of the front image with mode blob; active
// false keeps the display off (output power).
func (o *Output) modesetReq(blob uint32, active bool) *atomicReq {
	req := &atomicReq{}
	req.set(o.crtc, o.crtcProps["MODE_ID"], uint64(blob))
	req.set(o.crtc, o.crtcProps["ACTIVE"], boolValue(active))
	req.set(o.crtc, o.vrrProp, 0)
	req.set(o.conn.id, o.connCrtc, uint64(o.crtc))
	o.hdrConnectorProps(req, o.hdrOn)
	o.primaryProps(req, o.fbs[1-o.back])
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, cursorState{})
	}
	o.overlayProps(req, overlayWin{})
	for _, p := range o.stray {
		// Only planes a previous master left on this CRTC.
		props, err := o.k.objProps(p.id, objPlane)
		if err != nil || uint32(props["CRTC_ID"][1]) != o.crtc {
			continue
		}
		req.set(p.id, p.prop("FB_ID"), 0)
		req.set(p.id, p.prop("CRTC_ID"), 0)
	}
	return req
}

// probeAsync checks once whether async commits may carry IN_FENCE_FD.
func (o *Output) probeAsync(r ports.Renderer) {
	if o.asyncProbed || !o.tearing {
		return
	}
	o.asyncProbed = true
	r.UseTarget(o.back)
	done, err := r.Render(ports.Scene{Background: "#000000"}, nil)
	if err != nil {
		return
	}
	req := &atomicReq{}
	req.set(o.primary.id, o.primary.prop("FB_ID"), uint64(o.fbs[o.back]))
	if done != nil {
		defer done.Close()
		req.set(o.primary.id, o.primary.prop("IN_FENCE_FD"), uint64(done.Fd()))
	}
	err = o.k.commit(req, atomicTestOnly|flipAsyncFlag, 0)
	o.asyncFence = err == nil && done != nil
	o.log.Info().Err(err).Str("connector", o.conn.name).Bool("async_fence", o.asyncFence).Msg("tearing probe")
}

// wantVRR is the VRR state the next frame commit sets: on while game (a
// buffer is scanned out or a fullscreen window covers the output), off
// after vrrHold without it (each toggle may flicker, so short breaks keep
// it).
func (o *Output) wantVRR(game bool) bool {
	if o.vrrProp == 0 {
		return false
	}
	o.vrrGame = game
	if game {
		o.composedSince = time.Time{}
		return true
	}
	if o.composedSince.IsZero() {
		o.composedSince = time.Now()
	}
	return o.vrrOn && time.Since(o.composedSince) <= vrrHold
}

// stateVRR is the VRR state of a commit without a new frame (cursor,
// vrrOff timer): the last frame's. A shown buffer is not enough: it may
// be a tiled window on the overlay plane.
func (o *Output) stateVRR() bool { return o.wantVRR(o.vrrGame) }

// commitFrame flips fb in one commit with the cursor and VRR. An async
// commit carries only the primary plane (kernel rule): a frame that must
// also move the cursor or change VRR flips at vblank. fence is the
// frame's GPU fence (nil: none); the caller keeps and closes it.
func (o *Output) commitFrame(fb uint32, fence *os.File, async bool, vrr bool, f pendingFrame) error {
	return o.commitWith(fb, fence, async, vrr, f, overlayWin{})
}

// commitWith is commitFrame with ov on the overlay plane (zero: off).
func (o *Output) commitWith(fb uint32, fence *os.File, async bool, vrr bool, f pendingFrame, ov overlayWin) error {
	cur := cursorState{}
	if o.cursor != nil {
		cur = o.cursor.desired()
	}
	if async && (vrr != o.vrrOn || o.cursor != nil && cur != o.cursor.applied || fence != nil && !o.asyncFence || ov.buf != o.overlayOn) {
		async = false
	}
	req := &atomicReq{}
	flags := uint32(atomicNonblock | flipEventFlag)
	if async {
		flags |= flipAsyncFlag
		req.set(o.primary.id, o.primary.prop("FB_ID"), uint64(fb))
	} else {
		o.primaryProps(req, fb)
		req.set(o.crtc, o.vrrProp, boolValue(vrr))
		if o.cursor != nil {
			o.cursor.props(req, o.crtc, cur)
		}
		if ov.buf != 0 || o.overlayOn != 0 {
			o.overlayProps(req, ov)
		}
	}
	if fence != nil {
		req.set(o.primary.id, o.primary.prop("IN_FENCE_FD"), uint64(fence.Fd()))
	}
	// The kernel takes its own reference on the fence.
	if err := o.k.commit(req, flags, o.frame.userData(userFrame)); err != nil {
		if !async && cur.on && errors.Is(err, unix.EINVAL) && o.cursorRefused() {
			return o.commitWith(fb, fence, false, vrr, f, ov)
		}
		if !async && vrr && !o.vrrOn && errors.Is(err, unix.EINVAL) {
			// Retry without turning VRR on: if that passes, the driver
			// refuses VRR on this output. The overlay stays: the composed
			// image left its window out.
			if o.commitWith(fb, fence, false, false, f, ov) == nil {
				o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("vrr refused; disabled")
				o.vrrProp = 0
				return nil
			}
		}
		return err
	}
	if !async {
		if o.vrrOn != vrr {
			o.log.Info().Bool("vrr", vrr).Str("connector", o.conn.name).Msg("vrr")
		}
		o.vrrOn = vrr
	}
	if o.cursor != nil {
		// An async commit leaves the cursor plane as applied.
		o.cursor.committed(cur)
	}
	if !async {
		o.overlayOn = ov.buf
	}
	o.lastFrame, o.cursorHeld = time.Now(), false
	f.frame, f.async = true, async
	f.fences = dupFences(fence, ov.acquire)
	o.setAsync(async)
	o.frame.begin(f, time.Now())
	return nil
}

// cursorRefused reports, after a commit carrying the cursor failed with
// EINVAL, whether the cursor plane was the cause; then the cursor is off
// and the commit can be retried without it.
func (o *Output) cursorRefused() bool {
	o.testCursor()
	return o.cursor.off
}

// commitState commits a cursor or VRR change without a new frame.
func (o *Output) commitState(vrr bool) error {
	req := &atomicReq{}
	cur := cursorState{}
	// A move back to the applied state holds nothing.
	o.cursorHeld = false
	if o.cursor != nil {
		cur = o.cursor.desired()
		if cur != o.cursor.applied {
			if o.cursorWaits(time.Now()) {
				o.cursorHeld = true
				cur = o.cursor.applied
			} else {
				o.cursor.props(req, o.crtc, cur)
			}
		}
	}
	if vrr != o.vrrOn {
		req.set(o.crtc, o.vrrProp, boolValue(vrr))
	}
	if len(req.objs) == 0 {
		return nil
	}
	if err := o.k.commit(req, atomicNonblock|flipEventFlag, o.frame.userData(userState)); err != nil {
		if o.overlayConflict(err, o.overlayOn) {
			// The next frame is composed without the overlay.
			return errOverlayDropped
		}
		if cur.on && errors.Is(err, unix.EINVAL) && o.cursorRefused() {
			return o.commitState(vrr)
		}
		return err
	}
	if o.cursor != nil {
		o.cursor.committed(cur)
	}
	if vrr != o.vrrOn {
		o.log.Info().Bool("vrr", vrr).Str("connector", o.conn.name).Msg("vrr")
	}
	o.vrrOn = vrr
	o.frame.begin(pendingFrame{}, time.Now())
	return nil
}

// cursorMinInterval bounds how long a held cursor move waits for a frame.
const cursorMinInterval = time.Second / 24

// cursorWaits reports whether a cursor move rides on the next frame
// instead of its own commit. Under VRR a cursor-only commit refreshes the
// panel at its slowest rate, which delays the game's next frame by up to
// one such refresh. A game that stops drawing still gets the cursor at
// cursorMinInterval.
func (o *Output) cursorWaits(now time.Time) bool {
	return o.vrrOn && o.vrrGame && now.Sub(o.lastFrame) < cursorMinInterval
}

func boolValue(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// commitScanout flips a client buffer. It reports false when KMS refused
// the buffer: the frame must be composed.
func (o *Output) commitScanout(fb uint32, c ports.SurfaceContent, f pendingFrame) (bool, error) {
	cfb := o.clientFBs[c.DMABuf.ID]
	// Drivers refuse async flips that change the format or modifier: the
	// flip from the composed image into scanout waits for vblank.
	async := c.Async && o.tearing && o.shown != 0 && !cfb.noAsync
	vrr := o.wantVRR(true)
	f.queued, f.zeroCopy = c.DMABuf.ID, c.ID
	// Explicit sync: KMS waits on the client's acquire fence. Wayland may
	// close its file at any time: commit a duplicate taken while it is
	// held open (the kernel keeps its own reference).
	fence := dupFence(c.Acquire)
	if fence != nil {
		defer fence.Close()
	}
	err := o.commitFrame(fb, fence, async, vrr, f)
	if err != nil && async && errors.Is(err, unix.EINVAL) {
		// Refused (e.g. not a fast update): this buffer flips at vblank
		// from now on.
		o.log.Debug().Err(err).Str("connector", o.conn.name).Msg("async flip refused")
		cfb.noAsync = true
		err = o.commitFrame(fb, fence, false, vrr, f)
	}
	if err != nil && (errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE)) {
		// KMS refuses this buffer on the plane: compose it instead.
		o.log.Info().Str("component", "render").Err(err).Uint32("format", c.DMABuf.Format).Uint64("modifier", c.DMABuf.Modifier).Msg("scanout flip refused")
		cfb.failed = "flip_refused"
		o.setScanoutReason("flip_refused")
		return false, nil
	}
	if err == nil {
		o.queued = c.DMABuf.ID
	}
	return true, err
}

// vrrHold is how long composition runs before VRR turns off.
const vrrHold = 500 * time.Millisecond

// setAsync logs when the output starts or stops tearing.
func (o *Output) setAsync(on bool) {
	if on != o.async {
		o.async = on
		o.log.Info().Bool("tearing", on).Str("connector", o.conn.name).Msg("tearing")
	}
}

// Close restores the CRTC state found at startup, when it had a mode, and
// frees buffers.
func (o *Output) Close() {
	req := &atomicReq{}
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, cursorState{})
	}
	o.overlayProps(req, overlayWin{})
	o.hdrConnectorProps(req, false)
	req.set(o.crtc, o.vrrProp, 0)
	s := o.saved
	var blob uint32
	p := o.primary
	restored := false
	if s.crtcID != 0 && s.modeValid != 0 && s.fbID != 0 {
		if b, err := o.modeBlobFor(s.mode); err == nil {
			blob, restored = b, true
			req.set(o.crtc, o.crtcProps["MODE_ID"], uint64(b))
			req.set(o.crtc, o.crtcProps["ACTIVE"], 1)
			req.set(o.conn.id, o.connCrtc, uint64(o.crtc))
			req.set(p.id, p.prop("FB_ID"), uint64(s.fbID))
			req.set(p.id, p.prop("CRTC_ID"), uint64(o.crtc))
			req.set(p.id, p.prop("SRC_X"), 0)
			req.set(p.id, p.prop("SRC_Y"), 0)
			req.set(p.id, p.prop("SRC_W"), uint64(s.mode.HDisplay)<<16)
			req.set(p.id, p.prop("SRC_H"), uint64(s.mode.VDisplay)<<16)
			req.set(p.id, p.prop("CRTC_X"), 0)
			req.set(p.id, p.prop("CRTC_Y"), 0)
			req.set(p.id, p.prop("CRTC_W"), uint64(s.mode.HDisplay))
			req.set(p.id, p.prop("CRTC_H"), uint64(s.mode.VDisplay))
		}
	}
	if !restored {
		// Nothing to give back: leave the CRTC off.
		req.set(o.crtc, o.crtcProps["ACTIVE"], 0)
		req.set(o.crtc, o.crtcProps["MODE_ID"], 0)
		req.set(p.id, p.prop("FB_ID"), 0)
		req.set(p.id, p.prop("CRTC_ID"), 0)
		req.set(o.conn.id, o.connCrtc, 0)
	}
	// A live metadata blob means an HDR modeset may still be on screen, even
	// after a failed SDR fallback (hdrOn is then false): it is freed only once
	// an SDR modeset succeeds.
	hdrShown := o.hdrOn || o.hdrBlob != 0
	// Best effort: at exit the card may already belong to another session.
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		ev := o.log.Debug()
		if hdrShown {
			// The display may stay in HDR mode until the next modeset.
			ev = o.log.Warn()
		}
		ev.Err(err).Str("connector", o.conn.name).Bool("hdr", hdrShown).Msg("restore crtc")
	} else if hdrShown {
		o.log.Info().Str("connector", o.conn.name).Msg("HDR10 off")
	}
	if blob != 0 {
		_ = o.k.destroyBlob(blob)
	}
	if o.modeBlob != 0 {
		_ = o.k.destroyBlob(o.modeBlob)
		o.modeBlob = 0
	}
	if o.hdrBlob != 0 {
		_ = o.k.destroyBlob(o.hdrBlob)
		o.hdrBlob = 0
	}
	if o.cursor != nil {
		o.cursor.free(o.k)
	}
	o.freeImages()
	o.frame.endPending()
	o.dropRead()
	o.shown, o.queued = 0, 0
	o.dropClientFBs(time.Now(), true)
}

// Run renders scenes and commits them until ctx ends. active reports seat
// enable/disable. What the output shows and read is reported on presented.
func (o *Output) Run(ctx context.Context, newRenderer func(w, h int) (ports.Renderer, error), loadCursor CursorLoader, active <-chan bool, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, presented chan<- ports.OutputPresented, captures <-chan ports.CaptureRequest, captured chan<- ports.CaptureDone) (runErr error) {
	defer func() {
		p := recover()
		if p != nil {
			runErr = fmt.Errorf("output panic: %v", p)
		}
		if o.ready != nil && !o.readySent {
			o.ready <- runErr
			o.readySent = true
		}
		if p != nil {
			panic(p)
		}
	}()
	r, err := newRenderer(o.Width(), o.Height())
	if err != nil {
		return fmt.Errorf("create renderer: %w", err)
	}
	defer r.Close()
	if o.cursor != nil {
		if err := o.cursor.setup(o.k, r); err != nil {
			o.cursor.off = true
			o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("no hardware cursor images; hardware cursor off")
		}
	}
	// The seat sends its state first; while switched away (e.g. a monitor
	// plugged in on another VT) the modeset waits for the enable.
	enabled := true
	select {
	case enabled = <-active:
	default:
	}
	// Output images, from the best to the linear fallback (ADR 014).
	// While switched away they are made on the first enable.
	if enabled {
		if err := o.showImages(r, imagesDriver, nil); err != nil {
			return err
		}
		o.probeAsync(r)
	}
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	frame := 0
	var requests []ports.CaptureRequest
	ctx, cancelCaptures := context.WithCancel(ctx)
	defer func() {
		cancelCaptures()
		for _, q := range requests {
			capture.Fail(ctx, q, fmt.Errorf("output stopped"), captured)
		}
		for {
			select {
			case q := <-captures:
				capture.Fail(ctx, q, fmt.Errorf("output stopped"), captured)
			default:
				return
			}
		}
	}()
	clk := o.clock
	if clk == nil {
		clk = clock.System{}
	}
	stats := clk.NewTicker(10 * time.Second)
	defer stats.Stop()
	// Timer channels are enabled only when their condition holds. Go 1.27
	// guarantees a stopped/reset timer cannot deliver its old value.
	retryTimer := time.NewTimer(time.Hour)
	vrrTimer := time.NewTimer(time.Hour)
	cursorTimer := time.NewTimer(time.Hour)
	o.frame.startTimer()
	retryTimer.Stop()
	vrrTimer.Stop()
	cursorTimer.Stop()
	defer retryTimer.Stop()
	defer vrrTimer.Stop()
	defer cursorTimer.Stop()
	defer o.frame.stopTimer()
	// seen is the latest content Seq per window. It is reported only while
	// no frame is in flight: then the GPU has finished every frame that
	// read older buffers (the kernel waited for their fence to flip).
	seen := map[ports.WindowID]uint64{}
	reportDirty := false
	var cursorWake <-chan struct{}
	if o.cursor != nil {
		cursorWake = o.cursor.wake
	}
	for {
		readWait := false
		if reportDirty && !o.frame.pendingCommit() {
			if readWait = !o.readDone(); !readWait {
				o.report(nil, seen)
				reportDirty = false
			}
		}
		o.flushReport(presented)
		// An unsent report is retried soon, not only on the next event;
		// so is one that waits for an uncommitted frame's fence.
		var retry <-chan time.Time
		if len(o.unsent) > 0 || readWait {
			retryTimer.Reset(time.Millisecond)
			retry = retryTimer.C
		} else {
			retryTimer.Stop()
		}
		// A composed screen that stopped changing still drops VRR on time.
		var vrrOff <-chan time.Time
		if enabled && o.vrrOn && !o.frame.pendingCommit() && !o.composedSince.IsZero() {
			vrrTimer.Reset(max(0, vrrHold-time.Since(o.composedSince)))
			vrrOff = vrrTimer.C
		} else {
			vrrTimer.Stop()
		}
		// A held cursor move commits alone if no frame comes in time.
		var cursorDue <-chan time.Time
		if enabled && o.cursorHeld && !o.frame.pendingCommit() {
			cursorTimer.Reset(max(0, cursorMinInterval-time.Since(o.lastFrame)))
			cursorDue = cursorTimer.C
		} else {
			cursorTimer.Stop()
		}
		// A commit whose event never comes must not stop the output.
		stuck := o.frame.wait(enabled)
		stateDirty := false
		select {
		case <-retry:
			continue
		case <-stuck:
			if !o.frame.deadlineReached(time.Now()) {
				continue // waits for a fence
			}
			if o.expire() {
				if err := o.modeset(); err != nil {
					if !o.commitFailed(err, &enabled) {
						return fmt.Errorf("recovery modeset: %w", err)
					}
					continue
				}
			}
			dirty = haveScene
			stateDirty = true
		case <-vrrOff:
			stateDirty = true
		case <-cursorDue:
			stateDirty = true
		case <-cursorWake:
			stateDirty = true
		case <-ctx.Done():
			return nil
		case q := <-captures:
			if !enabled || o.off || o.wantOff {
				capture.Fail(ctx, q, fmt.Errorf("output off"), captured)
			} else {
				requests = append(requests, q)
				dirty = haveScene
			}
		case on := <-active:
			// Modeset on every enable: a fast disable+enable can coalesce to one true.
			if on {
				var err error
				if o.fbs[0] == 0 {
					err = o.showImages(r, imagesDriver, nil)
				} else {
					err = o.modeset()
				}
				if lostMaster(err) {
					// The seat took DRM master back: wait for the next enable.
					o.log.Warn().Err(err).Msg("resume")
					continue
				}
				if err != nil {
					o.log.Error().Err(err).Msg("resume")
					return err
				}
				o.probeAsync(r)
				dirty = haveScene
				stateDirty = true
			}
			enabled = on
			o.log.Info().Bool("enabled", on).Msg("output")
		case ev := <-o.flipped:
			if !o.completed(ev, seen) {
				continue // stale event from before a modeset
			}
			stateDirty = true
			if o.cursor != nil {
				// An image that waited for a free slot loads now.
				if _, err := o.cursor.flushLater(r); err != nil {
					o.log.Warn().Err(err).Msg("cursor")
				}
			}
		case s := <-scenes:
			if o.cursor != nil && loadCursor != nil && s.Scale != cursorScale {
				cursorScale = s.Scale
				o.setCursor(r, loadCursor, want, s.Scale)
				stateDirty = true
			}
			scene, haveScene, dirty = s, true, true
			o.wantOff = s.Off
		case c := <-cursor:
			want = c
			// Before the first scene the scale is unknown: loaded then.
			if o.cursor != nil && loadCursor != nil && cursorScale > 0 {
				o.setCursor(r, loadCursor, want, cursorScale)
				stateDirty = true
			}
		case c := <-contents:
			if c.Empty() {
				delete(surfaces, c.ID)
			} else {
				surfaces[c.ID] = c
			}
			if c.Seq > seen[c.ID] {
				seen[c.ID] = c.Seq
				reportDirty = true
			}
			// Windows on other outputs or workspaces do not need a frame.
			dirty = dirty || scene.Shows(c.ID)
		case <-stats.C():
			now := clk.Now()
			o.dropClientFBs(now, false)
			// An idle output renders nothing: free what windows that left
			// it held.
			if err := r.Trim(now); err != nil {
				return fmt.Errorf("trim renderer: %w", err)
			}
			ev := o.log.Info().Str("connector", o.conn.name).Int("frames", frame).Int("flips", o.flips).Bool("pending", o.frame.pendingCommit())
			if o.cursor != nil {
				cs := o.cursor.TakeStats()
				ev = ev.Int("cursor_moves", cs.Moves).Int("cursor_commits", cs.Commits)
			}
			ev.Msg("stats")
		}
		if len(requests) > 0 && (!enabled || o.off || o.wantOff) {
			for _, q := range requests {
				capture.Fail(ctx, q, fmt.Errorf("output off"), captured)
			}
			requests = nil
		}
		if !enabled || o.frame.pendingCommit() {
			continue
		}
		// Output power: off waits for no commit in flight; on is a modeset
		// and a full frame.
		if o.wantOff != o.off {
			var err error
			if o.wantOff {
				err = o.powerOff()
			} else if err = o.modeset(); err == nil {
				dirty = true
			}
			if err != nil && !o.commitFailed(err, &enabled) {
				return err
			}
		}
		if o.off {
			continue
		}
		if !dirty || !haveScene {
			if stateDirty {
				err := o.commitState(o.stateVRR())
				if errors.Is(err, errOverlayDropped) {
					dirty = haveScene
					continue
				}
				if err != nil && !o.commitFailed(err, &enabled) {
					return fmt.Errorf("commit: %w", err)
				}
			}
			continue
		}
		start := time.Now()
		direct, err := o.submitFrame(ctx, r, scene, surfaces, seen, requests, captured)
		requests = nil
		var fatal renderError
		if errors.As(err, &fatal) {
			return err
		}
		if err != nil {
			if errors.Is(err, errOverlayDropped) {
				dirty = true
				continue
			}
			if !o.commitFailed(err, &enabled) {
				return fmt.Errorf("page flip: %w", err)
			}
			continue // dirty stays true
		}
		dirty = false
		frame++
		o.log.Debug().Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Bool("direct", direct).Dur("took", time.Since(start)).Msg("frame")
	}
}

// commitFailed handles a commit error that does not stop the output: a
// commit still in flight (EBUSY, retried after its event) or DRM master
// lost mid-switch (the seat disable follows).
func (o *Output) commitFailed(err error, enabled *bool) bool {
	switch {
	case errors.Is(err, unix.EBUSY):
		// Any CRTC event can unblock a refusal; absent one, retry on time.
		if o.frame.busy(time.Now()) {
			o.log.Info().Err(err).Str("connector", o.conn.name).Msg("commit busy")
		}
		return true
	case lostMaster(err):
		o.log.Warn().Err(err).Msg("commit refused")
		*enabled = false
		return true
	}
	o.log.Error().Err(err).Msg("commit")
	return false
}

// completed handles the event of the pending commit: a flipped frame is
// reported with its kernel timestamp. It reports false for an event of
// another commit, which is dropped.
func (o *Output) completed(ev flipEvent, seen map[ports.WindowID]uint64) bool {
	f, start, ok := o.frame.flip(ev.user)
	if !ok {
		return false
	}
	if age := time.Since(start); age > 20*time.Millisecond {
		o.log.Info().Dur("flip_ms", age).Bool("frame", f.frame).Bool("vrr", o.vrrOn).Str("connector", o.conn.name).Msg("slow flip")
	}
	if o.cursor != nil {
		o.cursor.landed()
	}
	if !f.frame {
		return true
	}
	if f.composed {
		// Frames render in order: this flip's fence signalled after
		// those of frames rendered before it and not committed.
		o.dropRead()
	}
	o.flips++
	o.shown = f.queued
	if o.queued == f.queued {
		o.queued = 0
	}
	refresh := time.Duration(0)
	if !o.vrrOn {
		refresh = time.Duration(int64(time.Second) * 1000 / int64(max(1, o.mode.refreshMilli())))
	}
	o.report(&ports.FlipInfo{When: ev.when, Seq: uint64(ev.seq), Refresh: refresh, ZeroCopy: f.zeroCopy, Async: f.async, HardwareClock: true, Shows: f.shows}, seen)
	return true
}

// setCursor loads a cursor for scale into the cursor images.
func (o *Output) setCursor(r ports.Renderer, load CursorLoader, c ports.CursorChange, scale float64) {
	if o.cursor.off {
		return
	}
	img, err := load(c, scale, o.cursor.Limit())
	if err == nil {
		err = o.cursor.setImage(r, img.Pixels, img.W, img.H, img.HotX, img.HotY)
	}
	if err != nil {
		o.log.Warn().Err(err).Msg("cursor")
		return
	}
	o.log.Debug().Float64("scale", scale).Str("shape", c.Shape).Bool("client", c.Image != nil).Int("w", img.W).Int("h", img.H).Msg("cursor image")
}

// maxUnsent bounds reports waiting for wayland; past it the oldest flip is
// folded into the next one.
const maxUnsent = 16

// report queues what the output shows and has read, so replaced client
// buffers can be released. A flip-less report merges into the newest
// queued one; flips are never merged unless maxUnsent is reached.
func (o *Output) report(flip *ports.FlipInfo, seen map[ports.WindowID]uint64) {
	if !maps.Equal(o.seenSnapshot, seen) {
		o.seenSnapshot = maps.Clone(seen)
	}
	r := ports.OutputPresented{Output: o.conn.name, Flip: flip, Shown: o.shown, Queued: o.queued, Seen: o.seenSnapshot}
	if n := len(o.unsent); n > 0 && (flip == nil || o.unsent[n-1].Flip == nil) {
		if flip == nil {
			r.Flip = o.unsent[n-1].Flip
		}
		o.unsent[n-1] = r
		return
	}
	o.unsent = append(o.unsent, r)
	if len(o.unsent) > maxUnsent {
		old, next := o.unsent[0].Flip, o.unsent[1].Flip
		if next != nil && old != nil {
			next.Merged += 1 + old.Merged
			// A snapshot can be shared with previously sent flips. Clone only
			// when a merge actually needs to add an absent window.
			cloned := false
			for id, seq := range old.Shows {
				if _, ok := next.Shows[id]; !ok {
					if !cloned {
						next.Shows = maps.Clone(next.Shows)
						cloned = true
					}
					if next.Shows == nil {
						next.Shows = make(map[ports.WindowID]uint64)
					}
					next.Shows[id] = seq
				}
			}
		}
		o.unsent = slices.Delete(o.unsent, 0, 1)
		o.log.Warn().Str("connector", o.conn.name).Msg("wayland is not reading output reports; flips merged")
	}
}

// flushReport sends queued reports without blocking.
func (o *Output) flushReport(presented chan<- ports.OutputPresented) {
	for len(o.unsent) > 0 {
		select {
		case presented <- o.unsent[0]:
			o.unsent = slices.Delete(o.unsent, 0, 1)
		default:
			return
		}
	}
}

// imageKind is how output images are made, best first.
type imageKind int

const (
	imagesDriver imageKind = iota // exported, modifier chosen by the driver
	imagesLinear                  // exported, linear
)

// showImages sets up images from kind on and modesets them. KMS may refuse
// an image only in a modeset: a TEST_ONLY modeset checks it first, and the
// next kind is tried on refusal. cause is why the
// previous kind failed, for the log.
func (o *Output) showImages(r ports.Renderer, kind imageKind, cause error) error {
	if o.hdrSettings.Enabled && o.hdr.Capable && !o.hdrFailed {
		o.hdrOn = true
		meta := hdrMetadata(o.monitor)
		// Reuse the live blob across VT resume. A changed EDID creates a
		// replacement, but the old one remains alive until KMS accepts it.
		oldBlob := o.hdrBlob
		created := false
		var err error
		if oldBlob == 0 || meta != o.hdrBlobData {
			var newBlob uint32
			newBlob, err = o.k.createBlob(meta.bytes())
			if err == nil {
				o.hdrBlob = newBlob
				created = true
			}
		}
		if err == nil {
			r.SetHDR(float64(o.hdrSettings.SDRBrightness))
			err = o.showImageKind(r, imagesDriver, nil)
		}
		if err == nil {
			if created {
				o.hdrBlobData = meta
				if oldBlob != 0 {
					_ = o.k.destroyBlob(oldBlob)
				}
			}
			o.log.Info().Str("connector", o.conn.name).Int("sdr_brightness", o.hdrSettings.SDRBrightness).Float64("max_luminance", o.hdr.MaxLuminance).Float64("max_frame_average", o.hdr.MaxFrameAverage).Msg("HDR10 on")
			return nil
		}
		if created && oldBlob != 0 {
			// Failed replacement: the existing modeset may still use it.
			_ = o.k.destroyBlob(o.hdrBlob)
			o.hdrBlob = oldBlob
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("HDR modeset unavailable; falling back to SDR")
		o.hdrFailed = true
		o.hdrOn = false
		o.freeImages()
		r.SetHDR(0)
		_, _ = r.ExportTargets(0, nil)
		// A failed test commit can leave the old HDR mode on screen;
		// retain its blob until the SDR modeset succeeds or Close restores it.
	} else {
		r.SetHDR(0)
	}
	err := o.showImageKind(r, kind, cause)
	if err == nil && o.hdrFailed && o.hdrBlob != 0 {
		// The SDR commit has completed; the old HDR metadata is no longer in use.
		_ = o.k.destroyBlob(o.hdrBlob)
		o.hdrBlob = 0
		o.hdrBlobData = hdrOutputMetadata{}
	}
	return err
}

func (o *Output) showImageKind(r ports.Renderer, kind imageKind, cause error) error {
	for {
		got, err := o.setupImages(r, kind, cause)
		if err != nil {
			return err
		}
		o.kind = got
		if err = o.testModeset(); err == nil {
			return o.modeset()
		}
		if !refused(err) || got == imagesLinear {
			o.freeImages()
			return err
		}
		o.log.Warn().Err(err).Str("connector", o.conn.name).Int("kind", int(got)).Msg("modeset refused the output images")
		o.freeImages()
		kind, cause = got+1, err
	}
}

// refused reports a modeset error meaning KMS rejects the images.
func refused(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE)
}

// lostMaster reports a commit error meaning the seat is switched away.
func lostMaster(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}

// setupImages gives the output the two renderer images it flips between,
// exported as dmabufs. There is no CPU fallback (ADR 014): a GPU that
// cannot export a scanout-capable image cannot drive the output.
func (o *Output) setupImages(r ports.Renderer, kind imageKind, cause error) (imageKind, error) {
	err := cause
	for ; kind <= imagesLinear; kind++ {
		mods := []uint64(nil)
		if o.hdrOn {
			for _, f := range o.primary.formats {
				if f.Format == fourccXR30 {
					mods = append(mods, f.Modifier)
				}
			}
			if len(mods) == 0 {
				return kind, fmt.Errorf("primary plane has no XRGB2101010 modifiers")
			}
			if kind == imagesLinear {
				if !slices.Contains(mods, uint64(0)) {
					continue
				}
				mods = []uint64{0}
			}
		} else if kind == imagesLinear {
			mods = []uint64{0}
		}
		if err = o.exportImages(r, mods); err == nil {
			return kind, nil
		}
		o.log.Info().Err(err).Str("connector", o.conn.name).Int("kind", int(kind)).Msg("output image export")
	}
	return imagesLinear, fmt.Errorf("%s: GPU cannot export scanout images (ADR 014): %w", o.conn.name, err)
}

// exportImages makes the renderer's exported targets the output images.
func (o *Output) exportImages(r ports.Renderer, mods []uint64) error {
	bufs, err := r.ExportTargets(len(o.fbs), mods)
	if err != nil {
		return err
	}
	for i := range bufs {
		if err == nil {
			format := uint32(fourccXRGB)
			if o.hdrOn {
				format = fourccXR30
			}
			o.fbs[i], err = o.k.addFB(&bufs[i], format)
		}
		bufs[i].Planes[0].File.Close()
	}
	// GPU memory starts undefined (old VRAM contents): clear both images
	// before the modeset shows one.
	for i := range o.fbs {
		if err != nil {
			break
		}
		r.UseTarget(i)
		var done *os.File
		if done, err = r.Render(ports.Scene{Background: "#000000"}, nil); done != nil {
			// The modeset is a blocking commit: wait for the clear.
			err = errors.Join(err, syncfile.Wait(context.Background(), done))
			done.Close()
		}
	}
	if err != nil {
		o.freeImages()
		_, _ = r.ExportTargets(0, nil)
		return err
	}
	o.log.Info().Str("connector", o.conn.name).Uint64("modifier", bufs[0].Modifier).Msg("zero-copy output")
	return nil
}

// freeImages removes the output images' framebuffers.
func (o *Output) freeImages() {
	for i, fb := range o.fbs {
		if fb != 0 {
			_ = o.k.rmFB(fb)
		}
		o.fbs[i] = 0
	}
}

// shownBy returns an immutable snapshot of the content Seq of drawn windows.
// The scratch map is private to the output; a snapshot may still be read by
// wayland after a subsequent flip, so never refill a previously sent map.
func (o *Output) shownBy(s ports.Scene, seen map[ports.WindowID]uint64) map[ports.WindowID]uint64 {
	if o.showsScratch == nil {
		o.showsScratch = make(map[ports.WindowID]uint64)
	}
	clear(o.showsScratch)
	for id, seq := range seen {
		if s.Shows(id) {
			o.showsScratch[id] = seq
		}
	}
	if !maps.Equal(o.showsSnapshot, o.showsScratch) {
		o.showsSnapshot = maps.Clone(o.showsScratch)
	}
	return o.showsSnapshot
}

func (o *Output) directShownBy(id ports.WindowID, seq uint64) map[ports.WindowID]uint64 {
	if len(o.directShowsSnapshot) != 1 || o.directShowsSnapshot[id] != seq {
		o.directShowsSnapshot = map[ports.WindowID]uint64{id: seq}
	}
	return o.directShowsSnapshot
}
