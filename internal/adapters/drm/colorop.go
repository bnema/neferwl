package drm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Plane colour pipelines (DRM_CLIENT_CAP_PLANE_COLOR_PIPELINE).
//
// On an HDR10 output the composed image is PQ-encoded BT.2020. A plain sRGB
// client buffer can still reach the screen without composition when the
// plane's colour pipeline does what the Vulkan HDR path does to it:
//
//	sRGB EOTF -> x SDRBrightness/80 -> BT.709 to BT.2020 -> PQ inverse EOTF
//
// (PQ 125: 1.0 is 80 nits). The kernel exposes pipelines as chains of colorop
// objects. The matcher below is driver-agnostic: it looks for these four
// operations in this order and needs every other operation to be bypassable.

// colorMode is what a plane's colour pipeline does to the buffer it shows.
type colorMode uint8

const (
	// colorBypass is the plane's COLOR_PIPELINE "Bypass": pixels go as they are.
	colorBypass colorMode = iota
	// colorSDRToPQ turns sRGB pixels into the output's PQ BT.2020 signal.
	colorSDRToPQ
)

// colorUse is a plane's colour mode with the format of the buffer it
// applies to (refusals are cached per format).
type colorUse struct {
	mode   colorMode
	format uint32
}

// Plane and colorop property names and the enum names the matcher looks for.
// Enum values are never hardcoded: they come from the property.
const (
	propColorPipeline = "COLOR_PIPELINE"

	colorTypeCurve      = "1D Curve"
	colorTypeMultiplier = "Multiplier"
	colorTypeMatrix     = "3x4 Matrix"
	curveSRGBEOTF       = "sRGB EOTF"
	curvePQInverseEOTF  = "PQ 125 Inverse EOTF"
)

// colorop is one operation of a pipeline as read from KMS: property IDs by
// role (0: the operation has none) and the enum of its curve.
type colorop struct {
	id                                  uint32
	typ                                 string
	bypass, curveProp, multiplier, data uint32
	curves                              map[string]uint64
}

// colorStage is an operation the output drives: its object, BYPASS and the
// property that configures it (CURVE_1D_TYPE, MULTIPLIER or DATA).
type colorStage struct {
	op, bypass, prop uint32
	// curve is the CURVE_1D_TYPE enum value of a curve stage.
	curve uint64
}

// colorPipeline is the plane's pipeline for SDR content on an HDR output.
type colorPipeline struct {
	// id is the COLOR_PIPELINE value: the pipeline's first colorop.
	id uint64
	// srgb, mult, ctm and pq are the four stages in pipeline order; others
	// are every other operation, which must stay bypassed.
	srgb, mult, ctm, pq colorStage
	others              []colorStage
}

// is reports whether the operation is the wanted type, with the wanted curve.
func (o colorop) is(typ, curve string) bool {
	if o.typ != typ {
		return false
	}
	switch typ {
	case colorTypeCurve:
		_, ok := o.curves[curve]
		return o.curveProp != 0 && ok
	case colorTypeMultiplier:
		return o.multiplier != 0
	case colorTypeMatrix:
		return o.data != 0
	}
	return false
}

// matchColorPipeline looks in ops, the pipeline starting at colorop id first,
// for a 1D curve with sRGB EOTF, a multiplier, a 3x4 matrix and a 1D curve
// with PQ 125 inverse EOTF, in this order. Any other operation must be
// bypassable. The reason says why a pipeline does not match.
func matchColorPipeline(first uint64, ops []colorop) (*colorPipeline, string) {
	want := [4]struct{ typ, curve string }{
		{colorTypeCurve, curveSRGBEOTF}, {colorTypeMultiplier, ""}, {colorTypeMatrix, ""}, {colorTypeCurve, curvePQInverseEOTF},
	}
	cp := &colorPipeline{id: first}
	stages := [4]*colorStage{&cp.srgb, &cp.mult, &cp.ctm, &cp.pq}
	n := 0
	for _, op := range ops {
		if n < len(want) && op.is(want[n].typ, want[n].curve) {
			s := stages[n]
			s.op, s.bypass = op.id, op.bypass
			switch n {
			case 0:
				s.prop, s.curve = op.curveProp, op.curves[curveSRGBEOTF]
			case 1:
				s.prop = op.multiplier
			case 2:
				s.prop = op.data
			case 3:
				s.prop, s.curve = op.curveProp, op.curves[curvePQInverseEOTF]
			}
			n++
			continue
		}
		if op.bypass == 0 {
			return nil, fmt.Sprintf("colorop %d (%s) cannot be bypassed", op.id, op.typ)
		}
		cp.others = append(cp.others, colorStage{op: op.id, bypass: op.bypass})
	}
	if n < len(want) {
		return nil, fmt.Sprintf("no %s -> %s -> %s -> %s chain", want[0].curve, want[1].typ, want[2].typ, want[3].curve)
	}
	return cp, ""
}

// maxColorops bounds a pipeline walk: a longer chain is a cycle or garbage.
const maxColorops = 32

// walkColorops reads the pipeline starting at colorop first, following NEXT.
func walkColorops(k kms, first uint32) ([]colorop, error) {
	var ops []colorop
	for id := first; id != 0; {
		if len(ops) >= maxColorops {
			return nil, fmt.Errorf("colorop chain longer than %d", maxColorops)
		}
		props, err := k.objProps(id, objColorop)
		if err != nil {
			return nil, fmt.Errorf("colorop %d: %w", id, err)
		}
		op := colorop{id: id, bypass: uint32(props["BYPASS"][0]), curveProp: uint32(props["CURVE_1D_TYPE"][0]), multiplier: uint32(props["MULTIPLIER"][0]), data: uint32(props["DATA"][0])}
		if t := props["TYPE"]; t[0] != 0 {
			types, err := k.propEnums(uint32(t[0]))
			if err != nil {
				return nil, fmt.Errorf("colorop %d type: %w", id, err)
			}
			for name, v := range types {
				if v == t[1] {
					op.typ = name
				}
			}
		}
		if op.curveProp != 0 {
			if op.curves, err = k.propEnums(op.curveProp); err != nil {
				return nil, fmt.Errorf("colorop %d curves: %w", id, err)
			}
		}
		ops = append(ops, op)
		id = uint32(props["NEXT"][1])
	}
	return ops, nil
}

// readColorPipeline returns the pipeline of p that does SDR to PQ, or why
// it has none. Call it once per plane (read at output creation).
func readColorPipeline(k kms, p *plane) (*colorPipeline, string) {
	prop := p.props[propColorPipeline]
	if prop == 0 {
		return nil, "no " + propColorPipeline + " property"
	}
	enums, err := k.propEnums(prop)
	if err != nil {
		return nil, err.Error()
	}
	// Name order is not stable: try the pipelines by colorop ID.
	var firsts []uint64
	for _, v := range enums {
		if v != 0 { // 0 is Bypass
			firsts = append(firsts, v)
		}
	}
	slices.Sort(firsts)
	var why []string
	for _, first := range firsts {
		ops, err := walkColorops(k, uint32(first))
		if err == nil {
			var cp *colorPipeline
			var reason string
			if cp, reason = matchColorPipeline(first, ops); cp != nil {
				return cp, ""
			}
			err = fmt.Errorf("%s", reason)
		}
		why = append(why, fmt.Sprintf("pipeline %d: %v", first, err))
	}
	if len(why) == 0 {
		return nil, "only Bypass"
	}
	return nil, strings.Join(why, "; ")
}

// bt709ToBT2020 converts linear BT.709 to BT.2020 primaries, row-major. It is
// the mat3 of vulkan/shaders/hdr.frag, which GLSL writes column by column.
var bt709ToBT2020 = [3][3]float64{
	{0.627404, 0.329283, 0.043313},
	{0.069097, 0.919540, 0.011362},
	{0.016391, 0.088013, 0.895595},
}

// s3132 encodes v as the S31.32 sign-magnitude fixed point of KMS colorops
// (not two's complement): bit 63 is the sign, the rest the magnitude.
func s3132(v float64) uint64 {
	m := uint64(math.Round(math.Abs(v) * (1 << 32)))
	if m > 1<<63-1 {
		m = 1<<63 - 1
	}
	if v < 0 {
		m |= 1 << 63
	}
	return m
}

// ctmBytes is the DATA blob of a 3x4 matrix colorop (struct
// drm_color_ctm_3x4): 12 S31.32 values, row-major; the fourth column, an
// offset, is zero.
func ctmBytes(m [3][3]float64) []byte {
	b := make([]byte, 0, 12*8)
	for _, row := range m {
		for _, v := range row {
			b = binary.LittleEndian.AppendUint64(b, s3132(v))
		}
		b = binary.LittleEndian.AppendUint64(b, 0)
	}
	return b
}

// planeColor decides how plane (with pipeline, nil: none) shows a client
// buffer of format whose content is c, on an output in HDR10 mode when hdrOn.
// opaque is the surface's Opaque. A buffer already in the output's encoding
// goes as it is; sRGB goes through the pipeline; anything else gets the reason
// it is composed.
func planeColor(c ports.SurfaceColor, format uint32, opaque, hdrOn bool, pipeline *colorPipeline) (colorMode, string) {
	switch {
	case isYUVFormat(format):
		return colorBypass, "yuv"
	case !hdrOn:
		// An SDR connector must never show raw PQ values.
		if c.IsPQ2020() {
			return colorBypass, "sdr_pq_content"
		}
		return colorBypass, ""
	case c.IsPQ2020():
		if !isTenBit(format) {
			return colorBypass, "hdr_format"
		}
		return colorBypass, ""
	case pipeline == nil || !opaque || !isSDRColor(c):
		// The kernel applies the pipeline to premultiplied pixels: exact
		// only without alpha.
		return colorBypass, "hdr_sdr_content"
	}
	return colorSDRToPQ, ""
}

// isSDRColor reports whether the surface is sRGB, set or unspecified: what the
// Vulkan path decodes with decodeSRGB. Extended linear and PQ are other encodings.
func isSDRColor(c ports.SurfaceColor) bool {
	if c.IsPQ2020() || c.IsExtendedLinear() {
		return false
	}
	return (c.TF == 0 || c.TF == ports.ColorTFSRGB) && (c.Primaries == 0 || c.Primaries == ports.ColorPrimariesSRGB)
}

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
// frame commit writes it. Every modeset, VT resume and the async probe call
// it, since the planes' state is then not ours to assume.
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
// showed the pipeline: when the same frame passes TEST_ONLY with Bypass, the
// pipeline is the cause. The refusal is cached for each plane that showed it,
// its buffer format and the cursor state, and the frame is not retried.
func (o *Output) colorRefused(err error, fb uint32, fence *os.File, vrr bool, cur cursorState, ov overlayWin, rect planeRect, pc colorUse) bool {
	if !o.colorUsed(pc, ov) {
		return false
	}
	bypassed := ov
	bypassed.color = colorUse{}
	if !o.contentRefused(fb, fence, vrr, cur, bypassed, rect, colorUse{}) {
		return false
	}
	if pc.mode != colorBypass {
		o.primary.setVerdict(pc.format, cur.on, false)
	}
	if ov.buf != 0 && ov.color.mode != colorBypass {
		o.overlay.setVerdict(ov.color.format, cur.on, false)
	}
	o.log.Info().Str("component", "render").Err(err).Str("connector", o.conn.name).Bool("cursor", cur.on).Msg("plane colour pipeline refused")
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
		o.log.Info().Str("component", "render").Err(err).Str("connector", o.conn.name).Msg("plane colour pipeline refused with the cursor")
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
		o.log.Info().Str("component", "render").Err(err).Uint32("format", format).Uint32("plane", p.id).Bool("cursor", cursor).Str("connector", o.conn.name).Msg("plane colour pipeline refused")
	}
	return err == nil
}

// setupColor makes the output's colour objects: the CTM blob and the
// multiplier, when HDR can be on and a plane has a matched pipeline. A
// failure drops the pipelines: SDR content is composed as before.
func (o *Output) setupColor() {
	if !o.hdr.wanted() {
		// The pipelines serve HDR outputs only: SDR stays on Bypass.
		o.primary.pipeline = nil
		if o.overlay != nil {
			o.overlay.pipeline = nil
		}
		return
	}
	if o.primary.pipelineOf() == nil && o.overlay.pipelineOf() == nil {
		return
	}
	blob, err := o.k.createBlob(ctmBytes(bt709ToBT2020))
	if err != nil {
		o.log.Warn().Str("component", "drm").Err(err).Str("connector", o.conn.name).Msg("colour matrix blob; plane colour pipelines off")
		o.primary.pipeline = nil
		if o.overlay != nil {
			o.overlay.pipeline = nil
		}
		return
	}
	o.ctmBlob = blob
	o.colorMult = s3132(float64(o.hdr.settings.SDRBrightness) / 80)
}

// pipelineOf is a nil-safe read of p.pipeline.
func (p *plane) pipelineOf() *colorPipeline {
	if p == nil {
		return nil
	}
	return p.pipeline
}

// readColor finds the planes' SDR to PQ pipelines, once at output creation.
func (o *Output) readColor() {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p == nil || !p.hasColor() {
			continue
		}
		cp, why := readColorPipeline(o.k, p)
		p.pipeline = cp
		o.log.Info().Str("component", "drm").Str("connector", o.conn.name).Uint32("plane", p.id).Bool("matched", cp != nil).Str("reason", why).Msg("plane colour pipeline")
	}
}

// colorUsed reports whether a frame with primary colour pc and overlay ov
// has a plane show the pipeline.
func (o *Output) colorUsed(pc colorUse, ov overlayWin) bool {
	return pc.mode != colorBypass || ov.buf != 0 && ov.color.mode != colorBypass
}
