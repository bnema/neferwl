package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/capture"
	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/presented"

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
	// Set before Run. The backend owns instance registration and wake routing.
	Security         ports.SessionSecurity
	SecurityChanges  <-chan ports.SecurityState
	Instance         ports.OutputInstance
	SecurityEvents   chan<- ports.SecurityBackendEvent
	securityState    ports.SecurityState
	securityPrepared bool
	securityInvalid  bool
	inactiveOnClose  bool            // affirmative terminal KMS result, owned by Close
	runContext       context.Context // bounds compositor clear waits during Run startup

	k       kms
	flipped <-chan flipEvent // commit events of this CRTC, from Card.ReadEvents
	crtc    uint32
	conn    connector
	mode    modeInfo
	saved   modeCrtc
	monitor Monitor
	hdr     hdrState
	log     zerowrap.Logger
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
	// ctmBlob is the 3x4 matrix blob of the planes' colour pipelines
	// (BT.709 to BT.2020), made at setup and destroyed by Close (0: none);
	// colorMult is the S31.32 multiplier, SDR white over 80 nits.
	ctmBlob   uint32
	colorMult uint64
	// fbs are the two renderer images frames alternate between, back the
	// one the next frame draws into (ADR 014: zero copy).
	fbs   [2]uint32
	back  int
	frame frameLifecycle // pending commit, serials, deadlines and timer
	flips int
	// NewCaptureRenderer makes the child renderer of a hidden workspace
	// capture session (Scene.CaptureScene). Nil: such captures fail closed.
	// Set before Run.
	NewCaptureRenderer func(w, h int) (ports.Renderer, error)
	// StartOff, set before Run, keeps the display off until a scene turns
	// it on: a display that reconnects while turned off never lights up.
	StartOff bool
	// capHidden limits a report to what the child renderer of a hidden
	// workspace was given, window by window, while it may still read (its
	// device is not ordered with the display's fences). capped is set when
	// the last report had child reads: it is repeated until they finish.
	capHidden func(map[ports.WindowID]uint64) (map[ports.WindowID]uint64, map[ports.WindowID]uint64, bool)
	capped    bool
	// readFences are fences of frames rendered but not committed: the
	// GPU may still read client buffers until they signal, so what was
	// seen is reported only then (or after a later frame flipped).
	readFences []*os.File
	// Direct scanout: client framebuffers by DMABuf ID and the buffer on
	// screen and queued (0: the composed image).
	scanout       bool
	clientFBs     map[uint64]*clientFB
	shown, queued uint64
	planeRect     planeRect
	reason        string // why the last frame was composed ("" = scanout)
	reports       presented.Queue
	// Last immutable shows snapshot sent in a flip. Never mutate it: wayland
	// may still be reading a prior report on another goroutine.
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
	contentProp   uint32
	contentValues [5]uint64
	contentValue  uint64
	contentWanted uint64
	composedSince time.Time
	// Reusable atomic requests, owned by the output goroutine (zero value
	// ready). frameReq builds frame commits (commitWithRect), which retry
	// by recursing: a retry resets it, so a caller never reads it after a
	// recursive call. stateReq builds commitState. Paths that hold a second
	// request while one is alive use their own: withoutReq (commitState's
	// retry test) and probeReq (contentRefused, the scale TEST_ONLY probe).
	frameReq, stateReq, withoutReq, probeReq atomicReq
	// restWindows is overlayFrame's scene windows, valid until its next call.
	restWindows []ports.SceneWindow
	// lastFrame is when the last frame was committed. cursorHeld: a cursor
	// move waits for the next frame (see cursorWaits).
	lastFrame  time.Time
	cursorHeld bool
	// traceFlips logs every completion with its timing; lastFlipAt is
	// the previous frame flip's kernel timestamp (always kept).
	// flipStats are reported and reset with the periodic stats. wantedAt
	// is when the output first needed a frame since the last frame commit
	// (0: none), taken by the commit into pendingFrame.
	traceFlips bool
	lastFlipAt time.Duration
	flipStats  flipStats
	wantedAt   time.Duration
	// vrrFlipGap is the minimum time between a game frame's flip event
	// and the next frame commit under VRR (render.vrr-flip-gap, 0: off);
	// flipGapUntil is when the next frame may commit: the gap from the
	// time the flip event was read, as the workaround was measured. See
	// vrr_flip_gap.go.
	vrrFlipGap   time.Duration
	flipGapUntil time.Time
	// flipGapAt is the end of the gap from the flip's kernel timestamp
	// (CLOCK_MONOTONIC), kept until the frame it held commits. It differs
	// from flipGapUntil on purpose: the flip stats must not hide a late
	// read of the event inside the gap.
	flipGapAt time.Duration
	// wantOff is the latest Scene.Off: a client turned the display off.
	// off: the CRTC is inactive for it. Every modeset (resume, recovery)
	// follows wantOff, so a display turned off never lights up.
	wantOff, off bool
	// protected is an output-owned latch. Ordinary modesets must not expose
	// a previous desktop target while it is set; only protectedModeset may
	// activate a target after clearing it and waiting for the GPU.
	protected bool
	// protectBackoff is the current retry delay after a refused protected
	// commit; protectNotBefore the earliest next attempt (zero: none).
	protectBackoff   time.Duration
	protectNotBefore time.Time
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

// CursorLoader returns the image of a cursor at an output scale, already
// rotated by the output transform t (the cursor plane is not rotated by the
// hardware), at most limit pixels on a side; an empty image hides the cursor.
type CursorLoader func(c ports.CursorChange, scale float64, t ports.BufferTransform, limit int) (ports.CursorImage, error)

// newOutput reads the CRTC's planes and properties for a connector on crtc.
func newOutput(card *Card, c connector, mode modeInfo, crtc uint32) (*Output, error) {
	log := card.log
	pipe := slices.Index(card.crtcs, crtc)
	o := &Output{k: card.k, flipped: card.flips[crtc], frame: frameLifecycle{serials: &card.serials}, formats: card.formats, sampled: card.want.Sampled, device: card.want.Device, crtc: crtc, conn: c, mode: mode, log: log, monitor: readMonitor(card.path, c.name), scanout: !card.want.NoScanout, clientFBs: map[uint64]*clientFB{}, reason: "start", ready: make(chan error, 1), traceFlips: card.want.TraceFlips, vrrFlipGap: card.want.VRRFlipGap}
	var err error
	if o.saved, err = getCrtc(card.fd, crtc); err != nil {
		log.Warn().Err(err).Uint32("crtc", crtc).Msg("save crtc; it will not be restored on exit")
	}
	if err := o.readProps(card.fd, pipe, card.taken, cursorSize(card.fd)); err != nil {
		return nil, err
	}
	o.hdr.settings = normalizedHDRSettings(card.want.HDR[c.name])
	o.hdr.cap = detectHDR(o.monitor, o.hdr.props)
	o.readColor()
	o.setupColor()
	log.Info().Str("connector", c.name).Bool("hdr_capable", o.hdr.cap.Capable).Str("reason", o.hdr.cap.Reason).Float64("max_luminance", o.hdr.cap.MaxLuminance).Float64("max_frame_average", o.hdr.cap.MaxFrameAverage).Float64("min_luminance", o.hdr.cap.MinLuminance).Msg("HDR capability")
	if o.hdr.settings.Enabled && !o.hdr.cap.Capable {
		log.Warn().Str("connector", c.name).Str("reason", o.hdr.cap.Reason).Msg("HDR requested but unavailable")
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
	o.hdr.props = readConnectorHDRProps(fd, np)
	o.contentProp, o.contentValues = readContentTypeProp(fd, np)
	o.contentValue = np["content type"][1]
	o.contentWanted = o.contentValue
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
	o.primaryRectProps(req, fb, fullPlaneRect(o.Width(), o.Height()))
}

func (o *Output) primaryRectProps(req *atomicReq, fb uint32, rect planeRect) {
	p := o.primary
	req.set(p.id, p.prop("FB_ID"), uint64(fb))
	req.set(p.id, p.prop("CRTC_ID"), uint64(o.crtc))
	for i, name := range [...]string{"SRC_X", "SRC_Y", "SRC_W", "SRC_H"} {
		req.set(p.id, p.prop(name), rect.src[i])
	}
	for i, name := range [...]string{"CRTC_X", "CRTC_Y", "CRTC_W", "CRTC_H"} {
		req.set(p.id, p.prop(name), rect.crtc[i])
	}
}

// modeset shows the front image with the output's mode, turning off every
// other plane on the CRTC; needed at start and after every VT resume.
// It blocks until the kernel applied it.
func (o *Output) modeset() error {
	o.observeSecurity()
	return o.modesetImage(o.fbs[1-o.back], !o.wantOff && !o.protected)
}

// modesetImage is called with an active protected image only by
// protectedModeset, after its clear fence has signalled.
func (o *Output) modesetImage(fb uint32, active bool) error {
	if o.protected && o.frame.pendingCommit() {
		return errProtectionPending
	}
	if o.protected && !active {
		if err := o.disableProtected(); err != nil {
			return err
		}
		o.frame.resetAfterModeset()
		o.forgetColor()
		o.planeRect = fullPlaneRect(o.Width(), o.Height())
		o.signalReady()
		return nil
	}
	blob, err := o.modeBlobFor(o.mode)
	if err != nil {
		if o.protected {
			return protectedCommitError{err}
		}
		return err
	}
	var req *atomicReq
	if o.protected {
		req = o.modesetBaseReq(blob, active)
		if err := o.detachProtectedPlanes(req); err != nil {
			_ = o.k.destroyBlob(blob)
			return protectedCommitError{err}
		}
		if active {
			o.primaryProps(req, fb)
		}
	} else {
		req = o.modesetReq(blob, active)
	}
	// Do not admit a desktop-front modeset after an engage observed while
	// constructing the request. Protected activation only contains black.
	wasProtected := o.protected
	o.observeSecurity()
	if active && !wasProtected && o.protected {
		_ = o.k.destroyBlob(blob)
		return errSecurityScene
	}
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		_ = o.k.destroyBlob(blob)
		if o.protected {
			return protectedCommitError{fmt.Errorf("modeset: %w", err)}
		}
		return fmt.Errorf("modeset: %w", err)
	}
	// Only now is nothing of ours pending or on screen: on failure the
	// previous buffers may still show.
	o.frame.resetAfterModeset()
	// Plane scaling and colour pipeline capabilities may change after a
	// modeset or VT resume; the planes' colour state is not assumed.
	for _, fb := range o.clientFBs {
		fb.scaleTestedOK, fb.scaleRefused = false, false
	}
	o.forgetColor()
	o.forgetColorVerdicts()
	o.shown, o.queued = 0, 0
	if o.modeBlob != 0 {
		_ = o.k.destroyBlob(o.modeBlob)
	}
	o.modeBlob = blob
	o.vrrOn, o.vrrGame, o.overlayOn, o.off = false, false, 0, !active
	// The gap and the flip interval belonged to the old state.
	o.flipGapUntil, o.flipGapAt, o.lastFlipAt = time.Time{}, 0, 0
	o.contentValue, o.contentWanted = o.contentValues[0], o.contentValues[0]
	o.planeRect = fullPlaneRect(o.Width(), o.Height())
	if o.cursor != nil {
		o.cursor.applied = cursorState{}
		o.cursor.screen, o.cursor.flying = 0, false
	}
	o.log.Info().Str("connector", o.conn.name).Bool("off", o.off).Msg("modeset")
	if !o.off && !o.protected {
		// An inactive CRTC tells nothing about the cursor plane. Protected
		// activation keeps it detached; do not test desktop cursor images.
		o.testCursor()
	}
	o.sendFormats()
	o.signalReady()
	return nil
}

// signalReady reports the first successful modeset once.
func (o *Output) signalReady() {
	if o.ready != nil && !o.readySent {
		o.ready <- nil
		o.readySent = true
	}
}

// powerOff turns the display off: the CRTC goes inactive and keeps its
// mode, as DPMS off. The commit blocks, so no event is pending after it;
// a modeset turns the display back on.
func (o *Output) powerOff() error {
	req := &atomicReq{}
	req.set(o.crtc, o.crtcProps["ACTIVE"], 0)
	req.set(o.crtc, o.vrrProp, 0)
	req.set(o.conn.id, o.contentProp, o.contentValues[0])
	o.forceBypass(req)
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		return fmt.Errorf("power off: %w", err)
	}
	o.colorBypassed()
	o.off, o.vrrGame = true, false
	o.setVRR(false)
	o.contentValue, o.contentWanted = o.contentValues[0], o.contentValues[0]
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
	if o.hdr.on && !o.off {
		f.HDR = &ports.OutputHDR{MaxLuminance: o.hdr.cap.MaxLuminance, MaxFrameAverage: o.hdr.cap.MaxFrameAverage, MinLuminance: o.hdr.cap.MinLuminance}
	}
	if o.scanout && !o.off {
		for _, format := range o.scanoutFormats(o.sampled) {
			// On HDR the 10-bit buffers go as they are; 8-bit ones are
			// offered too when the primary plane converts them (SDR
			// clients; HDR clients pick 10-bit formats for PQ).
			if !isYUVFormat(format.Format) && (isTenBit(format.Format) == o.hdr.on || o.hdr.on && o.primary.pipeline != nil) {
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

// modesetBaseReq sets the CRTC and connector without attaching any plane.
func (o *Output) modesetBaseReq(blob uint32, active bool) *atomicReq {
	req := &atomicReq{}
	req.set(o.crtc, o.crtcProps["MODE_ID"], uint64(blob))
	req.set(o.crtc, o.crtcProps["ACTIVE"], boolValue(active))
	req.set(o.crtc, o.vrrProp, 0)
	req.set(o.conn.id, o.contentProp, o.contentValues[0])
	req.set(o.conn.id, o.connCrtc, uint64(o.crtc))
	o.hdrConnectorProps(req, o.hdr.on)
	// A modeset starts from the planes' Bypass pipeline: whatever the
	// previous master left is not ours.
	o.forceBypass(req)
	return req
}

// modesetReq is the unlocked modeset of the front image with mode blob;
// active false keeps the display off (output power).
func (o *Output) modesetReq(blob uint32, active bool) *atomicReq {
	req := o.modesetBaseReq(blob, active)
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

// commitFrame flips fb in one commit with the cursor and VRR. An async
// commit carries only the primary plane (kernel rule): a frame that must
// also move the cursor or change VRR flips at vblank. fence is the
// frame's GPU fence (nil: none); the caller keeps and closes it.
func (o *Output) commitFrame(fb uint32, fence *os.File, async bool, vrr bool, f pendingFrame) error {
	return o.commitWithRect(fb, fence, async, vrr, f, overlayWin{}, fullPlaneRect(o.Width(), o.Height()), colorUse{})
}

// commitWith is commitFrame with ov on the overlay plane (zero: off). The
// primary plane shows a composed image: Bypass.
func (o *Output) commitWith(fb uint32, fence *os.File, async bool, vrr bool, f pendingFrame, ov overlayWin) error {
	return o.commitWithRect(fb, fence, async, vrr, f, ov, fullPlaneRect(o.Width(), o.Height()), colorUse{})
}

// commitWithRect commits fb on the primary plane at rect, with ov on the
// overlay, and the planes' colour pipelines as pc (primary) and ov.color
// (overlay) say. A frame that changes a plane's colour state is never async:
// only FB_ID may change in an async commit.
func (o *Output) commitWithRect(fb uint32, fence *os.File, async bool, vrr bool, f pendingFrame, ov overlayWin, rect planeRect, pc colorUse) error {
	o.observeSecurity()
	if o.Security != nil && (f.security != o.securityState || o.protected && !o.securityPrepared) {
		return errSecurityScene
	}
	cur := cursorState{}
	if o.cursor != nil && !o.protected {
		cur = o.cursor.desired()
	}
	if o.protected {
		async, vrr, ov, pc = false, false, overlayWin{}, colorUse{}
	}
	if async && (vrr != o.vrrOn || o.cursor != nil && cur != o.cursor.applied || fence != nil && !o.asyncFence || ov.buf != o.overlayOn || rect != o.planeRect || o.contentWanted != o.contentValue || o.colorStaleFor(pc, ov)) {
		async = false
	}
	req := &o.frameReq
	req.reset()
	flags := uint32(atomicNonblock | flipEventFlag)
	if async {
		flags |= flipAsyncFlag
		req.set(o.primary.id, o.primary.prop("FB_ID"), uint64(fb))
	} else {
		o.primaryRectProps(req, fb, rect)
		o.primaryColorProps(req, pc.mode)
		req.set(o.crtc, o.vrrProp, boolValue(vrr))
		if o.cursor != nil {
			o.cursor.props(req, o.crtc, cur)
		}
		if ov.buf != 0 || o.overlayOn != 0 {
			o.overlayProps(req, ov)
		} else {
			o.overlayColorProps(req, ov.color.mode)
		}
	}
	o.contentProps(req)
	if fence != nil {
		req.set(o.primary.id, o.primary.prop("IN_FENCE_FD"), uint64(fence.Fd()))
	}
	// Admission linearizes at this last defensive snapshot. An already
	// admitted native ioctl can complete, but precedes the black proof.
	o.observeSecurity()
	if o.Security != nil && (f.security != o.securityState || o.protected && !o.securityPrepared) {
		return errSecurityScene
	}
	// The kernel takes its own reference on the fence.
	if err := o.k.commit(req, flags, o.frame.userData(userFrame)); err != nil {
		// With a colour pipeline in the frame the cursor test cannot tell
		// the cursor from the pipeline: the caller composes instead.
		if !async && cur.on && errors.Is(err, unix.EINVAL) && !o.colorUsed(pc, ov) && o.cursorRefused() {
			return o.commitWithRect(fb, fence, false, vrr, f, ov, rect, pc)
		}
		if o.contentProp != 0 && o.contentWanted != o.contentValue && errors.Is(err, unix.EINVAL) && o.contentRefused(fb, fence, vrr, cur, ov, rect, pc) {
			o.log.Warn().Str("component", "drm").Err(err).Msg("content type refused; disabled")
			o.contentProp = 0
			return o.commitWithRect(fb, fence, false, vrr, f, ov, rect, pc)
		}
		if !async && vrr && !o.vrrOn && errors.Is(err, unix.EINVAL) {
			// Retry without turning VRR on: if that passes, the driver
			// refuses VRR on this output. The overlay stays: the composed
			// image left its window out.
			if o.commitWithRect(fb, fence, false, false, f, ov, rect, pc) == nil {
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
		o.setVRR(vrr)
	}
	if o.cursor != nil {
		// An async commit leaves the cursor plane as applied.
		o.cursor.committed(cur)
	}
	if !async {
		o.overlayOn = ov.buf
		o.colorCommitted(pc, ov)
	}
	o.contentValue = o.contentWanted
	o.planeRect = rect
	o.lastFrame, o.cursorHeld = time.Now(), false
	f.frame, f.async = true, async
	f.wantedAt, o.wantedAt = o.wantedAt, 0
	f.gapEnd, o.flipGapAt = o.flipGapAt, 0
	f.fences = dupFences(fence, ov.acquire)
	if o.traceFlips {
		f.fenceReady = signalled(f.fences)
	}
	o.setAsync(async)
	o.frame.begin(f, time.Now())
	return nil
}

// contentRefused tests the same frame without the connector hint before disabling it.
func (o *Output) contentRefused(fb uint32, fence *os.File, vrr bool, cur cursorState, ov overlayWin, rect planeRect, pc colorUse) bool {
	req := &o.probeReq // frameReq is still the caller's
	req.reset()
	o.primaryRectProps(req, fb, rect)
	o.primaryColorProps(req, pc.mode)
	req.set(o.crtc, o.vrrProp, boolValue(vrr))
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, cur)
	}
	if ov.buf != 0 || o.overlayOn != 0 {
		o.overlayProps(req, ov)
	} else {
		o.overlayColorProps(req, ov.color.mode)
	}
	if fence != nil {
		req.set(o.primary.id, o.primary.prop("IN_FENCE_FD"), uint64(fence.Fd()))
	}
	return o.k.commit(req, atomicTestOnly, 0) == nil
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
	o.observeSecurity()
	if o.protected {
		return errSecurityScene
	}
	req := &o.stateReq
	req.reset()
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
	o.contentProps(req)
	if len(req.objs) == 0 {
		return nil
	}
	o.observeSecurity()
	if o.protected {
		return errSecurityScene
	}
	if err := o.k.commit(req, atomicNonblock|flipEventFlag, o.frame.userData(userState)); err != nil {
		if o.colorConflict(err, o.cursor != nil && cur.on && cur != o.cursor.applied) {
			// A plane shows the pipeline and KMS refuses the cursor with
			// it: the next frame is composed.
			return errOverlayDropped
		}
		if o.overlayConflict(err, o.overlayOn) {
			// The next frame is composed without the overlay.
			return errOverlayDropped
		}
		if cur.on && errors.Is(err, unix.EINVAL) && o.cursorRefused() {
			return o.commitState(vrr)
		}
		if o.contentProp != 0 && o.contentWanted != o.contentValue && errors.Is(err, unix.EINVAL) {
			without := &o.withoutReq
			without.reset()
			if o.cursor != nil && cur != o.cursor.applied {
				o.cursor.props(without, o.crtc, cur)
			}
			if vrr != o.vrrOn {
				without.set(o.crtc, o.vrrProp, boolValue(vrr))
			}
			if len(without.objs) == 0 || o.k.commit(without, atomicTestOnly, 0) == nil {
				o.contentProp = 0
				o.log.Warn().Str("component", "drm").Err(err).Msg("content type refused; disabled")
				return o.commitState(vrr)
			}
		}
		return err
	}
	if o.cursor != nil {
		o.cursor.committed(cur)
	}
	if vrr != o.vrrOn {
		o.log.Info().Bool("vrr", vrr).Str("connector", o.conn.name).Msg("vrr")
	}
	o.setVRR(vrr)
	o.contentValue = o.contentWanted
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
	return o.commitScanoutRect(fb, c, f, fullPlaneRect(o.Width(), o.Height()), colorBypass)
}

// commitScanoutRect flips a client buffer at rect, shown with colour mode.
func (o *Output) commitScanoutRect(fb uint32, c ports.SurfaceContent, f pendingFrame, rect planeRect, mode colorMode) (bool, error) {
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
	pc := colorUse{mode: mode, format: c.DMABuf.Format}
	err := o.commitWithRect(fb, fence, async, vrr, f, overlayWin{}, rect, pc)
	if err != nil && async && errors.Is(err, unix.EINVAL) {
		// Refused (e.g. not a fast update): this buffer flips at vblank
		// from now on.
		o.log.Debug().Err(err).Str("connector", o.conn.name).Msg("async flip refused")
		cfb.noAsync = true
		err = o.commitWithRect(fb, fence, false, vrr, f, overlayWin{}, rect, pc)
	}
	if err != nil && mode != colorBypass && errors.Is(err, unix.EINVAL) && o.colorRefusedFrame(fb, rect) {
		// The buffer passes without the pipeline: the pipeline is what KMS
		// refuses (cached per plane, format and cursor state).
		o.primary.setVerdict(c.DMABuf.Format, o.cursorShown(), false)
		o.log.Info().Str("component", "render").Err(err).Uint32("format", c.DMABuf.Format).Str("connector", o.conn.name).Msg("scanout colour pipeline refused")
		o.setScanoutReason("color_refused")
		return false, nil
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

// Close frees buffers and disables scanout. Without a security gate it
// retains legacy restoration of the CRTC state found at startup; a wired
// gate never admits saved desktop pixels, even if its snapshot is unlocked.
func (o *Output) Close() {
	o.inactiveOnClose = false
	// Close may run after Run stopped, without receiving the engage wake.
	o.observeSecurity()
	req := &atomicReq{}
	if o.cursor != nil {
		o.cursor.props(req, o.crtc, cursorState{})
	}
	o.overlayProps(req, overlayWin{})
	// Whoever drives the planes next starts from Bypass.
	o.forceBypass(req)
	o.hdrConnectorProps(req, false)
	req.set(o.crtc, o.vrrProp, 0)
	s := o.saved
	var blob uint32
	p := o.primary
	restored := false
	if o.Security == nil && !o.protected && s.crtcID != 0 && s.modeValid != 0 && s.fbID != 0 {
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
	// Only the validated detachment path can certify terminal inactivity.
	// Legacy nil-gate restoration/disable remains best effort, not evidence.
	detached := false
	if o.protected || o.Security != nil {
		if err := o.detachProtectedPlanes(req); err != nil {
			o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("protected close plane detachment")
		} else {
			detached = o.primary != nil && o.crtc != 0 && o.crtcProps["ACTIVE"] != 0
		}
	}
	// A live metadata blob is freed only once an SDR modeset succeeds.
	hdrShown := o.hdr.shown()
	// Best effort: at exit the card may already belong to another session.
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		ev := o.log.Debug()
		if hdrShown {
			// The display may stay in HDR mode until the next modeset.
			ev = o.log.Warn()
		}
		ev.Err(err).Str("connector", o.conn.name).Bool("hdr", hdrShown).Msg("restore crtc")
	} else {
		active, hasActive := req.value(o.crtc, o.crtcProps["ACTIVE"])
		o.inactiveOnClose = !restored && detached && hasActive && active == 0
		if hdrShown {
			o.log.Info().Str("connector", o.conn.name).Msg("HDR10 off")
		}
	}
	if blob != 0 {
		_ = o.k.destroyBlob(blob)
	}
	if o.modeBlob != 0 {
		_ = o.k.destroyBlob(o.modeBlob)
		o.modeBlob = 0
	}
	o.hdr.releaseBlob(o.k)
	if o.ctmBlob != 0 {
		_ = o.k.destroyBlob(o.ctmBlob)
		o.ctmBlob = 0
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

// InactiveOnClose reports affirmative terminal inactivity, not cached power
// state. Call only after Close's owner has finished and a done channel (or
// equivalent owner-completion synchronization) has been observed. It is not
// safe to read concurrently with the output owner; no shared mutation is added.
func (o *Output) InactiveOnClose() bool { return o.inactiveOnClose }

// Run renders scenes and commits them until ctx ends. active reports seat
// enable/disable. What the output shows and read is reported on reportsCh.
func (o *Output) Run(ctx context.Context, newRenderer func(w, h int) (ports.Renderer, error), loadCursor CursorLoader, active <-chan bool, scenes <-chan ports.Scene, contents <-chan ports.SurfaceContent, cursor <-chan ports.CursorChange, reportsCh chan<- ports.OutputPresented, captures <-chan ports.CaptureRequest, captured chan<- ports.CaptureDone) (runErr error) {
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
	o.runContext = ctx
	o.wantOff = o.StartOff
	o.observeSecurity()
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
			// While protected, a recoverable KMS failure keeps the output
			// registered and dark; the loop retries with bounded backoff.
			if !o.protected || !o.commitFailed(err, &enabled) {
				return err
			}
		}
		if !o.protected {
			o.probeAsync(r)
		}
	}
	surfaces := make(map[ports.WindowID]ports.SurfaceContent)
	var scene ports.Scene
	haveScene, dirty := false, false
	var want ports.CursorChange
	cursorScale := -1.0 // not loaded yet
	var cursorTransform ports.BufferTransform
	frame := 0
	var requestStorage [capture.MaxRequests]ports.CaptureRequest
	requests := requestStorage[:0]
	ctx, cancelCaptures := context.WithCancel(ctx)
	pipeline := capture.NewPipeline(ctx, captured)
	pipeline.Security = o.Security
	if o.NewCaptureRenderer != nil {
		pipeline.EnableOffscreen(o.NewCaptureRenderer)
		o.capHidden = pipeline.CapHiddenSeen
	}
	defer func() {
		cancelCaptures()
		pipeline.Close(r)
		for _, q := range requests {
			if q.ID != 0 || q.Dst.File != nil {
				capture.Fail(ctx, q, fmt.Errorf("output stopped"), captured)
			}
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
	protectTimer := time.NewTimer(time.Hour)
	o.frame.startTimer()
	gapTimer := time.NewTimer(time.Hour)
	gapTimer.Stop()
	defer gapTimer.Stop()
	// A request waits for a scene that shows its capture indicator, at most
	// capture.HoldFor (see capture.Hold).
	holdTimer := time.NewTimer(time.Hour)
	holdTimer.Stop()
	defer holdTimer.Stop()
	retryTimer.Stop()
	vrrTimer.Stop()
	cursorTimer.Stop()
	protectTimer.Stop()
	defer protectTimer.Stop()
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
	securityChanges := o.SecurityChanges
	for {
		if o.observeSecurity() || haveScene && !o.sceneCurrent(scene) {
			scene, haveScene, dirty = ports.Scene{}, false, false
			o.wantedAt = 0
			want, cursorScale, cursorTransform = ports.CursorChange{}, -1, 0
			if o.cursor != nil {
				o.cursor.image, o.cursor.later = false, nil
			}
		}
		if o.protected && len(requests) > 0 {
			for _, q := range requests {
				capture.Fail(ctx, q, fmt.Errorf("session protected"), captured)
			}
			clear(requests)
			requests = requestStorage[:0]
		}
		if o.protected && enabled && !o.frame.pendingCommit() && !o.securityPrepared && !o.protectBackoffActive() {
			if err := o.prepareSecurity(ctx, r); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if !o.commitFailed(err, &enabled) {
					return err
				}
			}
			// Recheck immediately: a takeover during the clear needs its own
			// transaction, not another scene or wake to make progress.
			if enabled && !o.frame.pendingCommit() && !o.securityPrepared && !o.protectBackoffActive() {
				continue
			}
		}
		if o.securityInvalid {
			if err := o.sendSecurityInvalid(ctx); err != nil {
				return nil
			}
		}
		readWait := false
		if reportDirty || o.capped {
			published := false
			if !o.frame.pendingCommit() {
				if readWait = !o.readDone(); !readWait {
					o.report(nil, seen)
					reportDirty, published = false, true
					// A report limited by the child's reads is repeated soon.
					readWait = o.capped
				}
			}
			if !published && o.capped {
				// A display frame or fence that has not finished must not
				// keep a finished child read held: child holds do not expire.
				// Report them alone, at the last Seen that was safe.
				o.report(nil, o.reports.Seen())
				readWait = readWait || o.capped
			}
		}
		o.reports.Flush(reportsCh)
		// An unsent report is retried soon, not only on the next event;
		// so is one that waits for an uncommitted frame's fence.
		var retry <-chan time.Time
		if o.reports.Len() > 0 || readWait {
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
		if enabled && !o.protected && o.cursorHeld && !o.frame.pendingCommit() {
			cursorTimer.Reset(max(0, cursorMinInterval-time.Since(o.lastFrame)))
			cursorDue = cursorTimer.C
		} else {
			cursorTimer.Stop()
		}
		// A game frame waiting out the VRR flip gap commits when it ends.
		var gapDue <-chan time.Time
		if enabled && dirty && !o.frame.pendingCommit() && !o.flipGapUntil.IsZero() {
			gapTimer.Reset(max(0, time.Until(o.flipGapUntil)))
			gapDue = gapTimer.C
		} else {
			gapTimer.Stop()
		}
		var holdDue <-chan time.Time
		if len(requests) > 0 {
			var wait time.Duration
			requests, wait = pipeline.Expire(scene, requests, time.Now())
			if wait > 0 {
				holdTimer.Reset(wait)
				holdDue = holdTimer.C
			}
		}
		if holdDue == nil {
			holdTimer.Stop()
		}
		// A refused protected commit is retried after a bounded backoff.
		var protectDue <-chan time.Time
		if o.protected && enabled && !o.securityPrepared && o.protectBackoffActive() {
			protectTimer.Reset(max(0, time.Until(o.protectNotBefore)))
			protectDue = protectTimer.C
		} else {
			protectTimer.Stop()
		}
		// A commit whose event never comes must not stop the output.
		stuck := o.frame.wait(enabled)
		stateDirty := false
		select {
		case _, ok := <-securityChanges:
			if !ok {
				securityChanges = nil
			}
			continue // gate is authoritative, not the queued wake payload
		case <-retry:
			continue
		case <-protectDue:
			continue
		case <-stuck:
			o.observeSecurity()
			if !o.frame.deadlineReached(time.Now()) {
				continue // waits for a fence
			}
			if o.expire() {
				if o.protected {
					o.invalidateSecurity()
					continue // fresh black transaction at the top of the loop
				}
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
		case <-gapDue:
		case <-holdDue:
			continue
		case <-ctx.Done():
			return nil
		case b := <-pipeline.Completed():
			pipeline.Recycle(b, r)
			continue
		case b := <-pipeline.HiddenCompleted():
			pipeline.RecycleHidden(b)
			pipeline.Retire(scene)
			continue
		case q := <-captures:
			o.observeSecurity()
			if o.protected || !enabled || o.off || o.wantOff {
				capture.Fail(ctx, q, fmt.Errorf("output off"), captured)
			} else if len(requests) == cap(requests) {
				capture.Fail(ctx, q, fmt.Errorf("output capture batch full"), captured)
			} else {
				q.Since = time.Now()
				requests = append(requests, q)
				// Its indicator may already be on screen; else it waits for the
				// scene that shows it.
				dirty = dirty || haveScene && capture.IndicatorShown(scene, q)
			}
		case on := <-active:
			o.observeSecurity()
			if o.protected {
				o.invalidateSecurity()
				enabled = on
				if on && o.fbs[0] == 0 {
					if err := o.showImages(r, imagesDriver, nil); err != nil {
						if !o.commitFailed(err, &enabled) {
							return err
						}
					}
				}
				continue
			}
			// Modeset on every enable: a fast disable+enable can coalesce to one true.
			if on {
				enabled = true // this enable may supersede a previous master loss
				var err error
				if o.fbs[0] == 0 {
					err = o.showImages(r, imagesDriver, nil)
				} else {
					err = o.modeset()
				}
				if err != nil {
					if !o.commitFailed(err, &enabled) {
						return err
					}
					continue // epoch retry or wait disabled for master reacquisition
				}
				o.observeSecurity()
				if o.protected {
					continue
				}
				o.probeAsync(r)
				dirty = haveScene
				stateDirty = true
			}
			enabled = on
			o.log.Info().Bool("enabled", on).Msg("output")
		case ev := <-o.flipped:
			o.observeSecurity()
			if !o.completed(ev, seen) {
				continue // stale event from before a modeset
			}
			stateDirty = true
			if o.cursor != nil && !o.protected {
				// An image that waited for a free slot loads now.
				if _, err := o.cursor.flushLater(r); err != nil {
					o.log.Warn().Err(err).Msg("cursor")
				}
			}
		case s := <-scenes:
			o.observeSecurity()
			if !o.sceneCurrent(s) {
				continue
			}
			if o.protected {
				s.CaptureScene = nil
				if o.wantOff != s.Off {
					o.invalidateSecurity()
				}
			}
			if !o.protected && o.cursor != nil && loadCursor != nil && (s.Scale != cursorScale || s.Transform != cursorTransform) {
				cursorScale, cursorTransform = s.Scale, s.Transform
				o.setCursor(r, loadCursor, want, s.Scale, s.Transform)
				stateDirty = true
			}
			scene, haveScene, dirty = s, true, true
			o.wantOff = s.Off
		case c := <-cursor:
			o.observeSecurity()
			if o.protected {
				continue
			}
			want = c
			// Before the first scene the scale is unknown: loaded then.
			if o.cursor != nil && loadCursor != nil && cursorScale > 0 {
				o.setCursor(r, loadCursor, want, cursorScale, cursorTransform)
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
			dirty = dirty || capture.Shows(scene, c.ID)
		case <-stats.C():
			now := clk.Now()
			pipeline.Retire(scene) // an idle child of an ended session goes
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
			fs := o.takeFlipStats()
			ev = ev.Int("missed_vblanks", fs.missedVblanks).Float64("max_flip_interval_ms", ms(fs.maxInterval))
			ev = ev.Float64("max_commit_delay_ms", ms(fs.maxCommitDelay)).Float64("max_flip_to_read_ms", ms(fs.maxFlipToRead))
			ev = ev.Int("redrawn_pixels", r.TakeRedrawn())
			if o.traceFlips {
				ev = ev.Int("late_fences", fs.lateFences)
			}
			ev.Msg("stats")
		}
		// A frame wanted while the previous one is in flight is due at
		// that flip: accountFlip never counts it before.
		if dirty && haveScene && o.wantedAt == 0 {
			o.wantedAt = monotonic()
		}
		o.observeSecurity()
		if haveScene && !o.sceneCurrent(scene) {
			continue
		}
		if o.protected && o.wantOff != o.off {
			o.invalidateSecurity()
		}
		if o.protected && !o.securityPrepared {
			continue
		}
		if len(requests) > 0 && (o.protected || !enabled || o.off || o.wantOff) {
			for _, q := range requests {
				capture.Fail(ctx, q, fmt.Errorf("output off"), captured)
			}
			clear(requests)
			requests = requestStorage[:0]
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
				if !o.protected {
					o.probeAsync(r) // skipped while it started off
				}
			}
			if err != nil && !o.commitFailed(err, &enabled) {
				return err
			}
		}
		if o.off {
			continue
		}
		if !dirty || !haveScene {
			if stateDirty && !o.protected {
				err := o.commitState(o.stateVRR())
				if errors.Is(err, errSecurityScene) {
					continue
				}
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
		if !o.flipGapUntil.IsZero() {
			if time.Now().Before(o.flipGapUntil) {
				continue // gapDue commits it
			}
			o.flipGapUntil = time.Time{}
		}
		if !o.sceneCurrent(scene) {
			continue
		}
		start := time.Now()
		direct, err := o.submitFrame(ctx, r, scene, surfaces, seen, requests, pipeline)
		var fatal renderError
		if errors.As(err, &fatal) || errors.Is(err, errSecurityScene) {
			// A failed render or epoch rejection did not hand these requests to the worker.
			for _, q := range requests {
				if capture.Handed(q) {
					continue // already owned by the worker, or answered
				}
				capture.Fail(ctx, q, err, captured)
			}
			clear(requests) // held ones included: their frame is gone
		}
		// Requests still waiting for their indicator stay; the rest is gone.
		requests = capture.Waiting(requests, scene)
		if errors.As(err, &fatal) {
			return err
		}
		if err != nil {
			if errors.Is(err, errSecurityScene) {
				continue
			}
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
		if !o.traceFlips {
			continue
		}
		o.log.Debug().Int("frame", frame).Uint64("seq", scene.Seq).Int("windows", len(scene.Windows)).Bool("direct", direct).Dur("took", time.Since(start)).Msg("frame")
	}
}

// commitFailed handles a commit error that does not stop the output: a
// commit still in flight (EBUSY, retried after its event) or DRM master
// lost mid-switch (the seat disable follows).
func (o *Output) commitFailed(err error, enabled *bool) bool {
	switch {
	case errors.Is(err, errSecurityScene), errors.Is(err, errProtectionPending):
		return true // retry at the owner boundary, never certify stale work
	case errors.Is(err, unix.EBUSY):
		// Any CRTC event can unblock a refusal; absent one, retry on time.
		if o.frame.busy(time.Now()) {
			o.log.Info().Err(err).Str("connector", o.conn.name).Msg("commit busy")
		}
		if o.protected && !o.securityPrepared {
			o.deferProtection() // bounded: a busy protected attempt never spins
		}
		return true
	case retryableProtected(err):
		// Protection stays latched and the output stays registered, dark.
		// Never stop the output: that would remove it from Wayland while the
		// session stays locked. Retry later; no proof is sent meanwhile.
		o.deferProtection()
		o.log.Warn().Err(err).Str("connector", o.conn.name).Dur("retry_in", o.protectBackoff).Msg("protected commit refused; output stays protected, retrying")
		return true
	case lostMaster(err):
		o.observeSecurity()
		if o.protected {
			o.invalidateSecurity()
		}
		o.log.Warn().Err(err).Msg("commit refused")
		*enabled = false
		return true
	}
	o.log.Error().Err(err).Msg("commit")
	return false
}

// flipDone times the completion ev of the commit f made at start: the
// flip stats, the flip trace and the VRR flip gap. It never allocates
// with tracing off.
func (o *Output) flipDone(ev flipEvent, f pendingFrame, start time.Time, ours, trace bool, fenceAt time.Duration) {
	readAt := time.Now()
	now := monotonic()
	commitAt := now - readAt.Sub(start)
	if ours {
		o.accountRead(ev, now)
	}
	if trace {
		o.traceFlip(ev, f, commitAt, now, o.lastFlipAt, fenceAt)
	}
	o.accountFlip(ev, f.frame, f.dueAt(), commitAt, fenceAt)
	o.startFlipGap(f, ev.when, readAt)
}

// completed handles the event of the pending commit: a flipped frame is
// reported with its kernel timestamp. It reports false for an event of
// another commit, which is dropped.
func (o *Output) completed(ev flipEvent, seen map[ports.WindowID]uint64) bool {
	// Only a commit of ours is traced: not a stale event, nor the end of
	// an EBUSY wait. Its fences are read before flip closes them.
	ours := o.frame.ours(ev.user)
	trace := o.traceFlips && ours
	var fenceAt time.Duration
	if trace {
		fenceAt = fencesSignalledAt(o.frame.pendingFrame.fences)
	}
	f, start, ok := o.frame.flip(ev.user)
	if !ok {
		return false
	}
	o.flipDone(ev, f, start, ours, trace, fenceAt)
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
	o.report(&ports.FlipInfo{When: ev.when, Seq: uint64(ev.seq), Refresh: o.refreshPeriod(), ZeroCopy: f.zeroCopy, Async: f.async, HardwareClock: true, Shows: f.shows}, seen)
	return true
}

// setCursor loads a cursor for scale and output transform t into the cursor
// images.
func (o *Output) setCursor(r ports.Renderer, load CursorLoader, c ports.CursorChange, scale float64, t ports.BufferTransform) {
	o.observeSecurity()
	if o.protected || o.cursor.off {
		return
	}
	img, err := load(c, scale, t, o.cursor.Limit())
	if err == nil {
		err = o.cursor.setImage(r, img.Pixels, img.W, img.H, img.HotX, img.HotY)
	}
	if err != nil {
		o.log.Warn().Err(err).Float64("scale", scale).Int("transform", int(t)).Msg("cursor")
		return
	}
	if c.Image != nil {
		// Client cursors (games) reload on every change: too many to log.
		return
	}
	o.log.Debug().Float64("scale", scale).Int("transform", int(t)).Str("shape", c.Shape).Int("w", img.W).Int("h", img.H).Msg("cursor image")
}

// refused reports a modeset error meaning KMS rejects the images.
func refused(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE)
}

// lostMaster reports a commit error meaning the seat is switched away.
func lostMaster(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}
