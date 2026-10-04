package drm

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// The output side of the plane colour pipelines: per-plane state, commit
// properties, refusal caching. The pure part (reading and matching
// pipelines, the maths, planeColor) is in colorop_match.go.

// hasColor reports whether the plane has a COLOR_PIPELINE property.
func (p *plane) hasColor() bool { return p != nil && p.props[propColorPipeline] != 0 }

// colorStale reports whether mode must be written: the plane applied
// another one, or nobody knows. A plane without a matched pipeline never
// leaves Bypass (modesets and Close write it), so it is never stale.
func (p *plane) colorStale(mode colorMode) bool {
	return p.hasColor() && p.pipeline != nil && (!p.colorKnown || p.colorApplied != mode)
}

// colorProps puts mode on p: Bypass selects the plane's Bypass pipeline;
// colorSDRToPQ selects the matched one with its four operations enabled and
// every other one bypassed. mult is the S31.32 multiplier and ctm the matrix
// blob.
func (p *plane) colorProps(req *atomicReq, mode colorMode, mult uint64, ctm uint32) {
	if !p.hasColor() {
		return
	}
	prop := p.props[propColorPipeline]
	cp := p.pipeline
	if mode != colorSDRToPQ || cp == nil {
		req.set(p.id, prop, 0)
		return
	}
	req.set(p.id, prop, cp.id)
	for _, s := range cp.others {
		req.set(s.op, s.bypass, 1)
	}
	for _, s := range [...]struct {
		stage colorStage
		val   uint64
	}{{cp.srgb, cp.srgb.curve}, {cp.mult, mult}, {cp.ctm, uint64(ctm)}, {cp.pq, cp.pq.curve}} {
		req.set(s.stage.op, s.stage.bypass, 0)
		req.set(s.stage.op, s.stage.prop, s.val)
	}
}

// colorVerdict is KMS's answer to a TEST_ONLY with the pipeline on a plane,
// kept per buffer format and cursor state.
type colorVerdict struct {
	format uint32
	cursor bool
	ok     bool
}

// verdict returns a cached answer.
func (p *plane) verdict(format uint32, cursor bool) (ok, known bool) {
	for _, v := range p.verdicts {
		if v.format == format && v.cursor == cursor {
			return v.ok, true
		}
	}
	return false, false
}

// setVerdict caches KMS's answer for the pipeline on p.
func (p *plane) setVerdict(format uint32, cursor, ok bool) {
	for i, v := range p.verdicts {
		if v.format == format && v.cursor == cursor {
			p.verdicts[i].ok = ok
			return
		}
	}
	p.verdicts = append(p.verdicts, colorVerdict{format: format, cursor: cursor, ok: ok})
}

// cursorShown reports whether a frame committed now shows the hardware cursor.
func (o *Output) cursorShown() bool { return o.cursor != nil && !o.protected && o.cursor.desired().on }

// primaryColorProps puts mode on the primary plane if it applies another one.
func (o *Output) primaryColorProps(req *atomicReq, mode colorMode) {
	if o.primary.colorStale(mode) {
		o.primary.colorProps(req, mode, o.colorMult, o.ctmBlob)
	}
}

// forceBypass sets Bypass on the primary and overlay planes whether or not
// the applied state is known to differ: for requests that must leave a
// known state (modeset, close).
func (o *Output) forceBypass(req *atomicReq) {
	o.primary.colorProps(req, colorBypass, 0, 0)
	o.overlay.colorProps(req, colorBypass, 0, 0)
}

// overlayColorProps puts mode on the overlay plane if it applies another one.
func (o *Output) overlayColorProps(req *atomicReq, mode colorMode) {
	if o.overlay.colorStale(mode) {
		o.overlay.colorProps(req, mode, o.colorMult, o.ctmBlob)
	}
}

// colorBypassed records that a commit left both planes on Bypass.
func (o *Output) colorBypassed() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p != nil {
			p.colorApplied, p.colorKnown = colorBypass, true
		}
	}
}

// forgetColor drops what is known about the planes' colour state: the next
// frame commit writes it. Every modeset (VT resume and recovery included)
// calls it, since the planes' state is then not ours to assume. The async
// probe follows a modeset and needs no call of its own.
func (o *Output) forgetColor() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p != nil {
			p.colorKnown = false
		}
	}
}

// forgetColorVerdicts drops the cached refusals: after a modeset the driver
// is asked again.
func (o *Output) forgetColorVerdicts() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p != nil {
			p.verdicts = p.verdicts[:0]
		}
	}
}

// colorStaleFor reports whether a frame commit with primary colour pc and
// overlay ov writes colour state: such a commit cannot be async.
func (o *Output) colorStaleFor(pc colorUse, ov overlayWin) bool {
	return o.primary.colorStale(pc.mode) || o.overlay.colorStale(ov.color.mode)
}

// colorCommitted records the colour state a successful synchronous frame
// commit left.
func (o *Output) colorCommitted(pc colorUse, ov overlayWin) {
	if o.primary.hasColor() {
		o.primary.colorApplied, o.primary.colorKnown, o.primary.colorFormat = pc.mode, true, pc.format
	}
	if o.overlay.hasColor() {
		o.overlay.colorApplied, o.overlay.colorKnown, o.overlay.colorFormat = ov.color.mode, true, ov.color.format
	}
}

// errColorRefused: KMS refused a frame with a plane colour pipeline that it
// takes with Bypass. The caller composes the frame.
var errColorRefused = errors.New("plane colour pipeline refused")

// colorRefused handles a frame commit that failed with EINVAL while a plane
// showed the pipeline: when the same frame passes TEST_ONLY with Bypass, and
// differs from the commit in colour alone, the pipeline is the cause. The refusal is cached for each plane that showed it,
// its buffer format and the cursor state, and the frame is not retried.
func (o *Output) colorRefused(err error, fb uint32, fence *os.File, vrr bool, cur cursorState, ov overlayWin, rect planeRect, pc colorUse) bool {
	if !o.colorUsed(pc, ov) {
		return false
	}
	bypassed := ov
	bypassed.color = colorUse{}
	// The same frame as the failed commit, content hint included, with
	// Bypass as the only difference: if the hint was the cause, this fails
	// too and the pipeline is not blamed.
	if !o.frameTest(fb, fence, vrr, cur, bypassed, rect, colorUse{}, true) {
		return false
	}
	if pc.mode != colorBypass {
		o.primary.setVerdict(pc.format, cur.on, false)
	}
	if ov.buf != 0 && ov.color.mode != colorBypass {
		o.overlay.setVerdict(ov.color.format, cur.on, false)
	}
	o.renderLog.Info().Err(err).Str("connector", o.conn.name).Bool("cursor", cur.on).Msg("plane colour pipeline refused")
	return true
}

// colorConflict handles a commit without a frame, which turned the cursor
// on or moved it, refused while a plane applies the pipeline: KMS may refuse
// that combination although it took each alone. The pipeline is refused for
// that buffer format with the cursor shown, so the next frame is composed
// (the cursor then commits with it). It reports whether it did.
func (o *Output) colorConflict(err error, cursorOn bool) bool {
	if !cursorOn || !errors.Is(err, unix.EINVAL) {
		return false
	}
	hit := false
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p.hasColor() && p.colorKnown && p.colorApplied == colorSDRToPQ {
			p.setVerdict(p.colorFormat, true, false)
			hit = true
		}
	}
	if hit {
		o.renderLog.Info().Err(err).Str("connector", o.conn.name).Msg("plane colour pipeline refused with the cursor")
	}
	return hit
}

// colorAllowed tests, once per plane, buffer format and cursor state, that
// KMS takes the SDR to PQ pipeline on p for the request test builds (the frame
// as it would commit). A refusal is cached: the buffer is composed from then
// on, never tested each frame.
func (o *Output) colorAllowed(p *plane, format uint32, test func(req *atomicReq)) bool {
	cursor := o.cursorShown()
	if ok, known := p.verdict(format, cursor); known {
		return ok
	}
	req := &o.probeReq
	req.reset()
	test(req)
	p.colorProps(req, colorSDRToPQ, o.colorMult, o.ctmBlob)
	if cursor {
		o.cursor.props(req, o.crtc, o.cursor.desired())
	}
	err := o.k.commit(req, atomicTestOnly, 0)
	if err != nil && !refused(err) {
		// Not an answer about the pipeline (e.g. master lost): ask again.
		return false
	}
	p.setVerdict(format, cursor, err == nil)
	if err != nil {
		o.renderLog.Info().Err(err).Uint32("format", format).Uint32("plane", p.id).Bool("cursor", cursor).Str("connector", o.conn.name).Msg("plane colour pipeline refused")
	}
	return err == nil
}

// setupColor makes the output's colour objects: the CTM blob and the
// multiplier, when HDR can be on and a plane has a matched pipeline. A
// failure drops the pipelines: SDR content is composed as before.
func (o *Output) setupColor() {
	if !o.hdr.wanted() {
		// The pipelines serve HDR outputs only: SDR stays on Bypass.
		o.dropPipelines()
		return
	}
	if o.primary.pipeline == nil && (o.overlay == nil || o.overlay.pipeline == nil) {
		return
	}
	blob, err := o.k.createBlob(ctmBytes(bt709ToBT2020))
	if err != nil {
		o.log.Warn().Err(err).Str("connector", o.conn.name).Msg("colour matrix blob; plane colour pipelines off")
		o.dropPipelines()
		return
	}
	o.ctmBlob = blob
	o.colorMult = s3132(float64(o.hdr.settings.SDRBrightness) / 80)
}

// dropPipelines leaves the output's planes without a pipeline: SDR content
// is composed.
func (o *Output) dropPipelines() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p != nil {
			p.pipeline = nil
		}
	}
}

// readColor finds the planes' SDR to PQ pipelines, once at output creation.
func (o *Output) readColor() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p == nil || !p.hasColor() {
			continue
		}
		cp, why := readColorPipeline(o.k, p)
		p.pipeline = cp
		o.log.Info().Str("connector", o.conn.name).Uint32("plane", p.id).Bool("matched", cp != nil).Str("reason", why).Msg("plane colour pipeline")
	}
}

// colorUsed reports whether a frame with primary colour pc and overlay ov
// has a plane show the pipeline.
func (o *Output) colorUsed(pc colorUse, ov overlayWin) bool {
	return pc.mode != colorBypass || ov.buf != 0 && ov.color.mode != colorBypass
}
