package drm

import (
	"encoding/binary"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// Plane COLOR_PIPELINE property IDs of the primary and overlay test planes,
// and the CRTC DEGAMMA_LUT.
const (
	pColorPipe, pColorPipeOverlay, pDegamma = 60, 61, 62
	opBase                                  = 200 // first colorop ID of a plane; a plane's ops use 20 IDs
)

// Property IDs of a colorop, by role: kernel properties are per object.
func bypassProp(op uint32) uint32 { return 1000 + op }
func curveProp(op uint32) uint32  { return 2000 + op }
func multProp(op uint32) uint32   { return 3000 + op }
func dataProp(op uint32) uint32   { return 4000 + op }
func typeProp(op uint32) uint32   { return 5000 + op }

// The enum values are arbitrary: the matcher must read names.
var (
	colorTypes = map[string]uint64{"1D Curve": 9, "1D LUT": 4, "3x4 Matrix": 2, "Multiplier": 6, "3D LUT": 1}
	firstCurve = map[string]uint64{"sRGB EOTF": 5, "PQ 125 EOTF": 6, "BT.2020 Inverse OETF": 3, "Gamma 2.2": 1}
	lastCurve  = map[string]uint64{"sRGB Inverse EOTF": 0, "PQ 125 Inverse EOTF": 8, "BT.2020 OETF": 2, "Gamma 2.2 Inverse": 7}
)

// opSpec is a colorop of a test pipeline.
type opSpec struct {
	typ      string
	curves   map[string]uint64
	noBypass bool
}

func curveOp(curves map[string]uint64) opSpec { return opSpec{typ: "1D Curve", curves: curves} }

var (
	opSRGB, opPQ = curveOp(firstCurve), curveOp(lastCurve)
	opMult       = opSpec{typ: "Multiplier"}
	opCTM        = opSpec{typ: "3x4 Matrix"}
	opLUT1D      = opSpec{typ: "1D LUT"}
	opLUT3D      = opSpec{typ: "3D LUT"}
	// amdgpuChain is the pipeline read on kernel 7.2 (DCN3+).
	amdgpuChain = []opSpec{opSRGB, opMult, opCTM, opPQ, opLUT1D, opLUT3D, curveOp(map[string]uint64{"sRGB EOTF": 5}), opLUT1D}
)

// colorPlane gives p a COLOR_PIPELINE property (id pipeProp) offering the
// chains, each with ops from base up, and registers the kernel's answers.
// It returns the first colorop of each chain.
func colorPlane(k *mockkms, p *plane, pipeProp, base uint32, chains ...[]opSpec) []uint64 {
	p.props = maps0(p.props)
	p.props["COLOR_PIPELINE"] = pipeProp
	enums := map[string]uint64{"Bypass": 0}
	var firsts []uint64
	k.EXPECT().propEnums(uint32(typeProp(0))).Return(colorTypes, nil).Maybe()
	id := base
	for c, chain := range chains {
		firsts = append(firsts, uint64(id))
		enums["Pipeline "+strconv.Itoa(c)] = uint64(id)
		for i, op := range chain {
			props := map[string][2]uint64{
				"TYPE": {uint64(typeProp(0)), colorTypes[op.typ]},
				"NEXT": {1, 0},
			}
			if i+1 < len(chain) {
				props["NEXT"] = [2]uint64{1, uint64(id + 1)}
			}
			if !op.noBypass {
				props["BYPASS"] = [2]uint64{uint64(bypassProp(id)), 1}
			}
			switch op.typ {
			case "1D Curve":
				props["CURVE_1D_TYPE"] = [2]uint64{uint64(curveProp(id)), 0}
				k.EXPECT().propEnums(curveProp(id)).Return(op.curves, nil).Maybe()
			case "Multiplier":
				props["MULTIPLIER"] = [2]uint64{uint64(multProp(id)), 0}
			case "3x4 Matrix", "1D LUT", "3D LUT":
				props["DATA"] = [2]uint64{uint64(dataProp(id)), 0}
			}
			k.EXPECT().objProps(id, uint32(objColorop)).Return(props, nil).Maybe()
			id++
		}
	}
	k.EXPECT().propEnums(pipeProp).Return(enums, nil).Maybe()
	return firsts
}

func maps0(m map[string]uint32) map[string]uint32 {
	out := make(map[string]uint32, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestMatchColorPipeline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chains [][]opSpec
		want   int    // index of the chain that matches, -1: none
		reason string // substring of the reason when none matches
	}{
		{"amdgpu", [][]opSpec{amdgpuChain}, 0, ""},
		{"minimal", [][]opSpec{{opSRGB, opMult, opCTM, opPQ}}, 0, ""},
		{"bypassable op before the chain", [][]opSpec{{opLUT1D, opSRGB, opMult, opCTM, opPQ}}, 0, ""},
		{"second pipeline matches", [][]opSpec{{opLUT1D}, amdgpuChain}, 1, ""},
		{"no pipeline", nil, -1, "only Bypass"},
		{"no PQ curve", [][]opSpec{{opSRGB, opMult, opCTM, curveOp(map[string]uint64{"sRGB Inverse EOTF": 0})}}, -1, "no sRGB EOTF"},
		{"no sRGB curve", [][]opSpec{{curveOp(map[string]uint64{"Gamma 2.2": 1}), opMult, opCTM, opPQ}}, -1, "no sRGB EOTF"},
		{"no multiplier", [][]opSpec{{opSRGB, opCTM, opPQ}}, -1, "no sRGB EOTF"},
		{"no matrix", [][]opSpec{{opSRGB, opMult, opPQ}}, -1, "no sRGB EOTF"},
		{"wrong order", [][]opSpec{{opSRGB, opCTM, opMult, opPQ}}, -1, "no sRGB EOTF"},
		{"PQ before sRGB", [][]opSpec{{opPQ, opMult, opCTM, opSRGB}}, -1, "no sRGB EOTF"},
		{"op without BYPASS", [][]opSpec{{opSRGB, opMult, opCTM, opPQ, {typ: "1D LUT", noBypass: true}}}, -1, "cannot be bypassed"},
		{"op without BYPASS first", [][]opSpec{{{typ: "3D LUT", noBypass: true}, opSRGB, opMult, opCTM, opPQ}}, -1, "cannot be bypassed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, k, _ := testOutput(t)
			firsts := colorPlane(k, o.primary, pColorPipe, opBase, tc.chains...)
			cp, why := readColorPipeline(k, o.primary)
			if tc.want < 0 {
				if cp != nil || !strings.Contains(why, tc.reason) {
					t.Fatalf("matched %+v, reason %q; want none with %q", cp, why, tc.reason)
				}
				return
			}
			if cp == nil {
				t.Fatalf("no match: %s", why)
			}
			if cp.id != firsts[tc.want] {
				t.Fatalf("pipeline id %d, want %d", cp.id, firsts[tc.want])
			}
			// The matched ops are the chain's, found by type, with their
			// own properties and the enum values read from the curves.
			chain, base := tc.chains[tc.want], uint32(cp.id)
			find := func(match func(opSpec) bool, from int) uint32 {
				for i := from; i < len(chain); i++ {
					if match(chain[i]) {
						return base + uint32(i)
					}
				}
				t.Fatal("test chain lacks an op")
				return 0
			}
			srgb := find(func(s opSpec) bool { return s.typ == "1D Curve" && s.curves["sRGB EOTF"] == 5 }, 0)
			mult := find(func(s opSpec) bool { return s.typ == "Multiplier" }, 0)
			ctm := find(func(s opSpec) bool { return s.typ == "3x4 Matrix" }, 0)
			pq := find(func(s opSpec) bool { _, ok := s.curves["PQ 125 Inverse EOTF"]; return ok }, 0)
			if cp.srgb != (colorStage{op: srgb, bypass: bypassProp(srgb), prop: curveProp(srgb), curve: 5}) ||
				cp.mult != (colorStage{op: mult, bypass: bypassProp(mult), prop: multProp(mult)}) ||
				cp.ctm != (colorStage{op: ctm, bypass: bypassProp(ctm), prop: dataProp(ctm)}) ||
				cp.pq != (colorStage{op: pq, bypass: bypassProp(pq), prop: curveProp(pq), curve: 8}) {
				t.Fatalf("stages %+v", cp)
			}
			if len(cp.others) != len(chain)-4 {
				t.Fatalf("%d other ops, want %d", len(cp.others), len(chain)-4)
			}
		})
	}
}

func TestReadColorPipelineWithoutProperty(t *testing.T) {
	o, k, _ := testOutput(t)
	if cp, why := readColorPipeline(k, o.primary); cp != nil || !strings.Contains(why, "COLOR_PIPELINE") {
		t.Fatalf("plane without the property: %+v %q", cp, why)
	}
}

// A NEXT loop must not hang the read.
func TestWalkColoropsStopsOnCycle(t *testing.T) {
	o, k, _ := testOutput(t)
	k.EXPECT().objProps(uint32(7), uint32(objColorop)).Return(map[string][2]uint64{"NEXT": {1, 7}}, nil)
	if _, err := walkColorops(k, 7); err == nil {
		t.Fatal("cyclic chain accepted")
	}
	_ = o
}

func TestS3132(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want uint64
	}{
		{0, 0},
		{math.Copysign(0, -1), 0},
		{1, 1 << 32},
		{2.5, 5 << 31},
		{0.5, 1 << 31},
		{-1, 1<<63 | 1<<32},
		{-0.5, 1<<63 | 1<<31},
		{203.0 / 80, uint64(math.Round(203.0 / 80 * (1 << 32)))},
		{1e30, 1<<63 - 1},
		{-1e30, 1<<63 | (1<<63 - 1)},
	} {
		if got := s3132(tc.v); got != tc.want {
			t.Errorf("s3132(%v) = %#x, want %#x", tc.v, got, tc.want)
		}
	}
}

// fromS3132 decodes sign-magnitude S31.32.
func fromS3132(v uint64) float64 {
	f := float64(v&(1<<63-1)) / (1 << 32)
	if v>>63 != 0 {
		return -f
	}
	return f
}

// The CTM is the mat3 of hdr.frag, transposed: GLSL lists columns, the blob
// is row-major with a zero fourth column.
func TestCTMBlobMatchesHDRShader(t *testing.T) {
	src, err := os.ReadFile("../vulkan/shaders/hdr.frag")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`mat3\(([^)]*)\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no mat3 in hdr.frag")
	}
	var cols []float64 // column-major, as GLSL reads it
	for _, f := range strings.Split(string(m[1]), ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil {
			t.Fatal(err)
		}
		cols = append(cols, v)
	}
	if len(cols) != 9 {
		t.Fatalf("mat3 has %d values", len(cols))
	}
	blob := ctmBytes(bt709ToBT2020)
	if len(blob) != 96 {
		t.Fatalf("blob is %d bytes, struct drm_color_ctm_3x4 is 96", len(blob))
	}
	for row := range 3 {
		for col := range 4 {
			got := fromS3132(binary.LittleEndian.Uint64(blob[(row*4+col)*8:]))
			want := 0.0
			if col < 3 {
				want = cols[col*3+row] // out[row] = sum over col of M[col][row]*in[col]
			}
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("blob[%d][%d] = %v, want %v", row, col, got, want)
			}
		}
	}
	// Applied to white, as the shader would: a column vector times mat3.
	var out [3]float64
	for row := range 3 {
		for col := range 3 {
			out[row] += fromS3132(binary.LittleEndian.Uint64(blob[(row*4+col)*8:]))
		}
	}
	for row, want := range [3]float64{
		cols[0] + cols[3] + cols[6], cols[1] + cols[4] + cols[7], cols[2] + cols[5] + cols[8],
	} {
		if math.Abs(out[row]-want) > 1e-9 {
			t.Errorf("white channel %d = %v, shader %v", row, out[row], want)
		}
	}
}

func TestPlaneColor(t *testing.T) {
	pq := ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}
	srgb := ports.SurfaceColor{TF: ports.ColorTFSRGB, Primaries: ports.ColorPrimariesSRGB}
	ext := ports.SurfaceColor{TF: ports.ColorTFExtendedLinear, Primaries: ports.ColorPrimariesSRGB}
	gamma := ports.SurfaceColor{TF: 10, Primaries: ports.ColorPrimariesSRGB}
	p3 := ports.SurfaceColor{TF: ports.ColorTFSRGB, Primaries: 7}
	pipe := &colorPipeline{}
	for _, tc := range []struct {
		name   string
		c      ports.SurfaceColor
		format uint32
		opaque bool
		hdr    bool
		pipe   *colorPipeline
		want   colorMode
		reason string
	}{
		{"SDR output, sRGB", ports.SurfaceColor{}, fourccXRGB, true, false, nil, colorBypass, ""},
		{"SDR output ignores a pipeline", srgb, fourccXRGB, true, false, pipe, colorBypass, ""},
		{"SDR output, PQ", pq, fourccXR30, true, false, nil, colorBypass, "sdr_pq_content"},
		{"SDR output, YUV", ports.SurfaceColor{}, fourccNV12, true, false, nil, colorBypass, "yuv"},
		{"HDR, PQ 10-bit", pq, fourccXR30, true, true, pipe, colorBypass, ""},
		{"HDR, PQ 10-bit with alpha", pq, fourccAR30, false, true, nil, colorBypass, ""},
		{"HDR, PQ 8-bit", pq, fourccXRGB, true, true, pipe, colorBypass, "hdr_format"},
		{"HDR, PQ YUV", pq, fourccP010, true, true, pipe, colorBypass, "yuv"},
		{"HDR, unspecified colour", ports.SurfaceColor{}, fourccXRGB, true, true, pipe, colorSDRToPQ, ""},
		{"HDR, sRGB", srgb, fourccXRGB, true, true, pipe, colorSDRToPQ, ""},
		{"HDR, sRGB, 10-bit buffer", srgb, fourccXR30, true, true, pipe, colorSDRToPQ, ""},
		{"HDR, sRGB, no pipeline", srgb, fourccXRGB, true, true, nil, colorBypass, "hdr_sdr_content"},
		{"HDR, sRGB with alpha", srgb, fourccARGB, false, true, pipe, colorBypass, "hdr_sdr_content"},
		{"HDR, extended linear", ext, fourccXRGB, true, true, pipe, colorBypass, "hdr_sdr_content"},
		{"HDR, other transfer function", gamma, fourccXRGB, true, true, pipe, colorBypass, "hdr_sdr_content"},
		{"HDR, other primaries", p3, fourccXRGB, true, true, pipe, colorBypass, "hdr_sdr_content"},
		{"HDR, sRGB YUV", srgb, fourccNV12, true, true, pipe, colorBypass, "yuv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, reason := planeColor(tc.c, tc.format, tc.opaque, tc.hdr, tc.pipe)
			if mode != tc.want || reason != tc.reason {
				t.Fatalf("mode %d reason %q, want %d %q", mode, reason, tc.want, tc.reason)
			}
		})
	}
}

// Colour test outputs.

// colorOutput is an HDR output whose primary (and overlay) plane matches the
// amdgpu pipeline. rule, when set, is the driver model every commit meets.
func colorOutput(t *testing.T, overlay bool, rule func(*atomicReq, uint32) error) (*Output, *mockkms, *[]commitRec) {
	t.Helper()
	o, k, commits, _ := testOutputRules(t, rule)
	props := maps0(planeProps)
	props["zpos"] = pZpos
	o.primary.props = props
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	colorPlane(k, o.primary, pColorPipe, opBase, amdgpuChain)
	if overlay {
		ov := &plane{id: tOverlay, typ: planeOverlay, props: props, zposValue: 2, formats: []ports.DMABufFormat{{Format: fourccXRGB}}}
		colorPlane(k, ov, pColorPipeOverlay, opBase+20, amdgpuChain)
		o.stray = []*plane{ov}
		o.pickOverlay()
		if o.overlay == nil {
			t.Fatal("no overlay")
		}
	}
	o.hdr.on = true
	o.readColor()
	o.ctmBlob, o.colorMult = 61, s3132(203.0/80)
	if o.primary.pipeline == nil || overlay && o.overlay.pipeline == nil {
		t.Fatal("pipeline not matched")
	}
	return o, k, commits
}

// colorOps are the colorop objects a commit touches.
func colorOps(c commitRec) []uint32 {
	var out []uint32
	for _, obj := range c.req.objs {
		if obj >= opBase {
			out = append(out, obj)
		}
	}
	return out
}

func sdrContent(o *Output) ports.SurfaceContent {
	o.clientFBs[9] = &clientFB{fbID: 80}
	return ports.SurfaceContent{ID: 1, Width: 200, Height: 100, Opaque: true, DMABuf: &ports.DMABuf{ID: 9, Format: fourccXRGB}}
}

func TestColorCommitTransitions(t *testing.T) {
	o, _, commits := colorOutput(t, false, nil)
	c := sdrContent(o)
	c.Async = true
	o.shown = 9
	cp := o.primary.pipeline
	last := func() commitRec { return (*commits)[len(*commits)-1] }

	// First SDR scanout writes the pipeline, never async even though the
	// buffer asks for it: only FB_ID may change in an async flip.
	if ok, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	first := last()
	if first.flags&flipAsyncFlag != 0 {
		t.Fatal("a colour change went async")
	}
	if v, ok := first.req.value(tPrimary, pColorPipe); !ok || v != cp.id {
		t.Fatalf("COLOR_PIPELINE %d %v, want %d", v, ok, cp.id)
	}
	for _, s := range []struct {
		op, prop uint32
		want     uint64
	}{
		{cp.srgb.op, cp.srgb.bypass, 0}, {cp.srgb.op, cp.srgb.prop, cp.srgb.curve},
		{cp.mult.op, cp.mult.bypass, 0}, {cp.mult.op, cp.mult.prop, s3132(203.0 / 80)},
		{cp.ctm.op, cp.ctm.bypass, 0}, {cp.ctm.op, cp.ctm.prop, 61},
		{cp.pq.op, cp.pq.bypass, 0}, {cp.pq.op, cp.pq.prop, cp.pq.curve},
	} {
		if v, ok := first.req.value(s.op, s.prop); !ok || v != s.want {
			t.Errorf("colorop %d prop %d = %d %v, want %d", s.op, s.prop, v, ok, s.want)
		}
	}
	for _, other := range cp.others {
		if v, ok := first.req.value(other.op, other.bypass); !ok || v != 1 {
			t.Errorf("other colorop %d BYPASS %d %v, want 1", other.op, v, ok)
		}
	}
	if !o.primary.colorKnown || o.primary.colorApplied != colorSDRToPQ {
		t.Fatalf("applied %v known %v", o.primary.colorApplied, o.primary.colorKnown)
	}

	// Same mode again: nothing is written, and the flip may be async.
	o.frame.endPending()
	o.vrrOn = true
	if ok, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if second := last(); second.flags&flipAsyncFlag == 0 || len(colorOps(second)) != 0 {
		t.Fatalf("steady state: flags %#x colorops %v", second.flags, colorOps(second))
	} else if _, ok := second.req.value(tPrimary, pColorPipe); ok {
		t.Fatal("steady state wrote COLOR_PIPELINE")
	}

	// A composed frame sets Bypass once.
	o.frame.endPending()
	if err := o.commitFrame(70, nil, true, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	composed := last()
	if composed.flags&flipAsyncFlag != 0 {
		t.Fatal("the return to Bypass went async")
	}
	if v, ok := composed.req.value(tPrimary, pColorPipe); !ok || v != 0 || len(colorOps(composed)) != 0 {
		t.Fatalf("composed: COLOR_PIPELINE %d %v colorops %v", v, ok, colorOps(composed))
	}
	o.frame.endPending()
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	again := last()
	if _, ok := again.req.value(tPrimary, pColorPipe); ok {
		t.Fatal("Bypass written twice")
	}
}

// A plane that applies the pipeline never sees colorop properties in a
// cursor or VRR commit.
func TestCommitStateNeverCarriesColorProps(t *testing.T) {
	o, _, commits := colorOutput(t, false, nil)
	o.primary.colorKnown, o.primary.colorApplied = false, colorSDRToPQ // stale on purpose
	o.cursor.image = true
	o.cursor.Move(3, 4)
	if err := o.commitState(true); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	if _, ok := c.req.value(tPrimary, pColorPipe); ok || len(colorOps(c)) != 0 {
		t.Fatalf("state commit carries colour: %+v", c.req)
	}
	if _, ok := c.req.value(tCursor, 14); !ok {
		t.Fatal("cursor not moved")
	}
}

// Protected frames, power off, Close and every modeset leave Bypass, on the
// overlay too, whatever was applied or known.
func TestColorBypassOnComposedProtectedOffCloseAndModeset(t *testing.T) {
	apply := func(o *Output) {
		for _, p := range []*plane{o.primary, o.overlay} {
			p.colorKnown, p.colorApplied = true, colorSDRToPQ
		}
	}
	bypassed := func(t *testing.T, c commitRec) {
		t.Helper()
		for _, p := range []struct{ plane, prop uint32 }{{tPrimary, pColorPipe}, {tOverlay, pColorPipeOverlay}} {
			if v, ok := c.req.value(p.plane, p.prop); !ok || v != 0 {
				t.Errorf("plane %d COLOR_PIPELINE %d %v, want Bypass", p.plane, v, ok)
			}
		}
		if ops := colorOps(c); len(ops) != 0 {
			t.Errorf("Bypass commit touches colorops %v", ops)
		}
	}
	t.Run("power off", func(t *testing.T) {
		o, _, commits := colorOutput(t, true, nil)
		apply(o)
		if err := o.powerOff(); err != nil {
			t.Fatal(err)
		}
		bypassed(t, (*commits)[0])
		if !o.primary.colorKnown || o.primary.colorApplied != colorBypass || o.overlay.colorApplied != colorBypass {
			t.Fatal("power off did not record Bypass")
		}
	})
	t.Run("close", func(t *testing.T) {
		o, k, commits := colorOutput(t, true, nil)
		k.EXPECT().rmFB(mock.Anything).Return(nil)
		k.EXPECT().destroyBlob(uint32(61)).Return(nil).Once()
		// Unknown state: Close still writes Bypass.
		o.Close()
		bypassed(t, (*commits)[0])
		if o.ctmBlob != 0 {
			t.Fatal("CTM blob kept")
		}
	})
	t.Run("modeset", func(t *testing.T) {
		o, k, commits := colorOutput(t, true, nil)
		k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
		k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, 0}}, nil)
		apply(o)
		o.primary.verdicts = []colorVerdict{{format: fourccXRGB}}
		if err := o.modeset(); err != nil {
			t.Fatal(err)
		}
		bypassed(t, (*commits)[0])
		if o.primary.colorKnown || o.overlay.colorKnown || len(o.primary.verdicts) != 0 {
			t.Fatal("a modeset kept the colour state or the refusals")
		}
	})
	t.Run("protected frame", func(t *testing.T) {
		o, _, commits := colorOutput(t, true, nil)
		apply(o)
		o.protected = true
		if err := o.commitWith(70, nil, false, false, pendingFrame{}, overlayWin{}); err != nil {
			t.Fatal(err)
		}
		c := (*commits)[0]
		if v, ok := c.req.value(tPrimary, pColorPipe); !ok || v != 0 {
			t.Fatalf("protected frame: COLOR_PIPELINE %d %v", v, ok)
		}
	})
}

// The overlay's pipeline is driven with the primary on Bypass; a frame
// without the overlay turns it back to Bypass once.
func TestColorOverlayTransitions(t *testing.T) {
	o, _, commits := colorOutput(t, true, nil)
	o.clientFBs[9] = &clientFB{fbID: 88}
	ov := overlayWin{id: 2, fb: 88, buf: 9, w: 100, h: 100, rect: ports.Rect{X: 100, W: 100, H: 100}, color: colorUse{mode: colorSDRToPQ, format: fourccXRGB}}
	if err := o.commitWith(70, nil, false, false, pendingFrame{}, ov); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	cp := o.overlay.pipeline
	if v, ok := c.req.value(tOverlay, pColorPipeOverlay); !ok || v != cp.id {
		t.Fatalf("overlay COLOR_PIPELINE %d %v, want %d", v, ok, cp.id)
	}
	if v, ok := c.req.value(tPrimary, pColorPipe); !ok || v != 0 {
		t.Fatalf("composed primary COLOR_PIPELINE %d %v, want Bypass", v, ok)
	}
	o.frame.endPending()
	if err := o.commitWith(70, nil, false, false, pendingFrame{}, ov); err != nil {
		t.Fatal(err)
	}
	if again := (*commits)[1]; len(colorOps(again)) != 0 {
		t.Fatal("overlay colour written twice")
	} else if _, ok := again.req.value(tOverlay, pColorPipeOverlay); ok {
		t.Fatal("overlay COLOR_PIPELINE written twice")
	}
	o.frame.endPending()
	if err := o.commitWith(70, nil, false, false, pendingFrame{}, overlayWin{}); err != nil {
		t.Fatal(err)
	}
	if v, ok := (*commits)[2].req.value(tOverlay, pColorPipeOverlay); !ok || v != 0 {
		t.Fatalf("overlay left without Bypass: %d %v", v, ok)
	}
}

// refusePipeline is a driver that refuses any request selecting a pipeline
// on a plane with EINVAL, TEST_ONLY included.
func refusePipeline(r *atomicReq, _ uint32) error {
	for _, obj := range [...]struct{ plane, prop uint32 }{{tPrimary, pColorPipe}, {tOverlay, pColorPipeOverlay}} {
		if v, ok := r.value(obj.plane, obj.prop); ok && v != 0 {
			return unix.EINVAL
		}
	}
	return nil
}

func hdrScanoutScene() (ports.Scene, map[ports.WindowID]ports.SurfaceContent) {
	s, c := fullscreenScene()
	return s, c
}

func countTests(commits []commitRec) int {
	n := 0
	for _, c := range commits {
		if c.flags&atomicTestOnly != 0 {
			n++
		}
	}
	return n
}

// SDR scanout on HDR is tested before its first use. A refusal is cached per
// plane, format and cursor state and is never tested again; a modeset asks
// the driver again.
func TestColorRefusalIsTestedOnceAndCached(t *testing.T) {
	o, k, commits := colorOutput(t, false, refusePipeline)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
	s, surfaces := hdrScanoutScene()
	decide := func() string {
		t.Helper()
		if fb, _, _ := o.scanoutFrame(s, surfaces); fb != 0 {
			t.Fatalf("refused pipeline scanned out (fb %d)", fb)
		}
		return o.reason
	}
	if got := decide(); got != "color_refused" || countTests(*commits) != 1 {
		t.Fatalf("reason %q after %d tests", got, countTests(*commits))
	}
	for range 3 {
		decide()
	}
	if n := countTests(*commits); n != 1 {
		t.Fatalf("a cached refusal was tested %d times", n)
	}
	if cfb := o.clientFBs[9]; cfb.failed != "" {
		t.Fatalf("buffer marked failed: %q", cfb.failed)
	}
	// The cursor shown is another state: asked once more, then cached.
	o.cursor.image = true
	o.cursor.Move(1, 1)
	decide()
	decide()
	if n := countTests(*commits); n != 2 {
		t.Fatalf("%d tests with the cursor shown, want 2", n)
	}
	// A modeset asks the driver again.
	o.forgetColorVerdicts()
	decide()
	if n := countTests(*commits); n != 3 {
		t.Fatalf("a modeset did not ask the driver again: %d tests", n)
	}
}

// KMS accepts the colour test and refuses the real commit: the frame is
// composed, the refusal cached, the buffer not blamed.
func TestColorRefusalOfCommittedFrame(t *testing.T) {
	// The first request that selects the pipeline outside TEST_ONLY fails.
	o, k, commits := colorOutput(t, false, func(r *atomicReq, flags uint32) error {
		if flags&atomicTestOnly == 0 {
			return refusePipeline(r, flags)
		}
		return nil
	})
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
	s, surfaces := hdrScanoutScene()
	d := o.decideFrame(s, surfaces, false)
	if d.fb != 80 || d.color != colorSDRToPQ {
		t.Fatalf("decision %+v", d)
	}
	ok, err := o.commitScanoutRect(d.fb, d.content, pendingFrame{}, d.rect, d.color)
	if ok || err != nil || o.reason != "color_refused" {
		t.Fatalf("ok=%v err=%v reason %q", ok, err, o.reason)
	}
	if o.frame.pendingCommit() || o.primary.colorKnown {
		t.Fatal("a refused commit changed the state")
	}
	before := len(*commits)
	if d := o.decideFrame(s, surfaces, false); d.fb != 0 || o.reason != "color_refused" || len(*commits) != before {
		t.Fatalf("refusal not cached: fb %d reason %q commits %d->%d", d.fb, o.reason, before, len(*commits))
	}
	if cfb := o.clientFBs[9]; cfb.failed != "" || cfb.noAsync {
		t.Fatalf("buffer blamed: %+v", cfb)
	}
}

// A buffer KMS refuses on its own is still blamed on the buffer, not the
// pipeline.
func TestColorRefusalOfTheBufferStaysTheBuffers(t *testing.T) {
	o, k, _ := colorOutput(t, false, func(r *atomicReq, flags uint32) error {
		// The driver takes the pipeline test, then refuses the buffer
		// whatever the pipeline: the commit and its Bypass test fail.
		if v, ok := r.value(tPrimary, pColorPipe); flags&atomicTestOnly == 0 || ok && v == 0 {
			return unix.EINVAL
		}
		return nil
	})
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
	s, surfaces := hdrScanoutScene()
	d := o.decideFrame(s, surfaces, false)
	if ok, err := o.commitScanoutRect(d.fb, d.content, pendingFrame{}, d.rect, d.color); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if cfb := o.clientFBs[9]; cfb.failed != "flip_refused" || o.reason != "flip_refused" {
		t.Fatalf("failed %q reason %q", cfb.failed, o.reason)
	}
}

// A cursor commit refused while a plane shows the pipeline caches the
// refusal for the cursor state and has the next frame composed.
func TestColorCursorConflict(t *testing.T) {
	o, _, _ := colorOutput(t, false, func(r *atomicReq, flags uint32) error {
		if v, ok := r.value(tCursor, pFB); ok && v != 0 {
			return unix.EINVAL
		}
		return nil
	})
	o.primary.colorKnown, o.primary.colorApplied, o.primary.colorFormat = true, colorSDRToPQ, fourccXRGB
	o.cursor.image = true
	o.cursor.Move(5, 5)
	if err := o.commitState(false); err != errOverlayDropped {
		t.Fatalf("commitState: %v", err)
	}
	if ok, known := o.primary.verdict(fourccXRGB, true); !known || ok {
		t.Fatal("refusal with the cursor not cached")
	}
	if o.cursor.off {
		t.Fatal("the cursor was blamed")
	}
}

// Overlay: allowed in HDR when the colour resolves; a refusal is cached.
func TestColorOverlayInHDR(t *testing.T) {
	o, k, commits := colorOutput(t, true, refusePipeline)
	o.primary.pipeline = nil // only the overlay converts
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(88), nil).Maybe()
	s, surfaces := overlayScene()
	// PQ content and SDR content without alpha are both candidates now.
	ov, rest := o.overlayFrame(s, surfaces)
	if ov.fb != 88 || ov.color.mode != colorSDRToPQ || len(rest.Windows) != 1 {
		t.Fatalf("overlay %+v", ov)
	}
	if o.testOverlay(70, ov) {
		t.Fatal("the driver's refusal was taken")
	}
	if o.overlayReason != "color_refused" {
		t.Fatalf("reason %q", o.overlayReason)
	}
	if cfb := o.clientFBs[9]; cfb.overlayFailed != "" {
		t.Fatalf("buffer blamed: %q", cfb.overlayFailed)
	}
	tests := countTests(*commits)
	if ov, rest := o.overlayFrame(s, surfaces); ov.fb != 0 || o.overlayReason != "color_refused" || len(rest.Windows) != 2 || countTests(*commits) != tests {
		t.Fatalf("not cached: %+v reason %q", ov, o.overlayReason)
	}
	// Without a pipeline an SDR window stays composed on HDR.
	o.overlay.pipeline = nil
	o.forgetColorVerdicts()
	if ov, _ := o.overlayFrame(s, surfaces); ov.fb != 0 || o.overlayReason != "no_candidate" {
		t.Fatalf("no pipeline: %+v reason %q", ov, o.overlayReason)
	}
}

func TestSetupColor(t *testing.T) {
	t.Run("HDR makes the matrix blob and the multiplier", func(t *testing.T) {
		o, k, _ := colorOutput(t, false, nil)
		o.ctmBlob, o.colorMult = 0, 0
		o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: 160}
		o.hdr.cap.Capable = true
		k.EXPECT().createBlob(mock.MatchedBy(func(b []byte) bool { return slices.Equal(b, ctmBytes(bt709ToBT2020)) })).Return(uint32(61), nil).Once()
		o.setupColor()
		if o.ctmBlob != 61 || o.colorMult != 2<<32 {
			t.Fatalf("blob %d multiplier %#x", o.ctmBlob, o.colorMult)
		}
	})
	t.Run("blob failure turns the pipelines off", func(t *testing.T) {
		o, k, _ := colorOutput(t, true, nil)
		o.ctmBlob = 0
		o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: 203}
		o.hdr.cap.Capable = true
		k.EXPECT().createBlob(mock.Anything).Return(uint32(0), unix.ENOMEM).Once()
		o.setupColor()
		if o.primary.pipeline != nil || o.overlay.pipeline != nil || o.ctmBlob != 0 {
			t.Fatal("pipelines kept without their blob")
		}
	})
	t.Run("SDR output keeps Bypass", func(t *testing.T) {
		o, _, _ := colorOutput(t, false, nil)
		o.ctmBlob = 0
		o.hdr.settings.Enabled = false
		o.setupColor()
		if o.primary.pipeline != nil || o.ctmBlob != 0 {
			t.Fatal("SDR output set up a pipeline")
		}
	})
}

// The cap is requested on the fd before anything is read; the property
// readers are shared with the content type.
func TestEnableColorPipelineFailsOnBadFD(t *testing.T) {
	if err := enableColorPipeline(-1); err == nil {
		t.Fatal("cap accepted on an invalid fd")
	}
}

// A frame refused because of its pipeline never turns the hardware cursor
// off: only a refusal the frame has with Bypass does.
func TestColorRefusalDoesNotDisableTheCursor(t *testing.T) {
	// The driver refuses the pipeline, and also the cursor whenever it is
	// shown with the pipeline.
	rule := func(r *atomicReq, flags uint32) error {
		if v, ok := r.value(tPrimary, pColorPipe); ok && v != 0 && flags&atomicTestOnly == 0 {
			return unix.EINVAL
		}
		return nil
	}
	o, k, _ := colorOutput(t, false, rule)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
	o.cursor.image = true
	o.cursor.Move(5, 5)
	c := sdrContent(o)
	ok, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ)
	if ok || err != nil || o.reason != "color_refused" {
		t.Fatalf("ok=%v err=%v reason %q", ok, err, o.reason)
	}
	if o.cursor.off {
		t.Fatal("a pipeline refusal turned the hardware cursor off")
	}
	if v, known := o.primary.verdict(fourccXRGB, true); !known || v {
		t.Fatal("refusal with the cursor not cached")
	}
	// A cursor the driver refuses on its own, pipeline or not, is still turned off.
	o2, k2, _ := colorOutput(t, false, func(r *atomicReq, flags uint32) error {
		if v, ok := r.value(tCursor, pFB); ok && v != 0 {
			return unix.EINVAL
		}
		return nil
	})
	k2.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
	o2.cursor.image = true
	o2.cursor.Move(5, 5)
	if ok, err := o2.commitScanoutRect(80, sdrContent(o2), pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !o2.cursor.off {
		t.Fatal("a cursor refusal did not turn the cursor off")
	}
}

// The colour test differs from the failed commit in colour alone. A frame
// refused for its content hint is not blamed on the pipeline; the hint is
// disabled as before. A frame refused for its pipeline, with the hint wanted,
// is blamed on the pipeline and the hint stays.
func TestColorRefusalBlamesOnlyColour(t *testing.T) {
	const pContent = 70
	setup := func(t *testing.T, rule func(*atomicReq, uint32) error) (*Output, ports.SurfaceContent) {
		o, k, _ := colorOutput(t, false, rule)
		k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(uint32(80), nil).Maybe()
		o.contentProp, o.contentValues = pContent, [5]uint64{0, 1, 2, 3, 4}
		o.contentWanted = 4 // wanted Game, the applied value is still the first
		return o, sdrContent(o)
	}
	hasHint := func(r *atomicReq) bool { v, ok := r.value(tConn, pContent); return ok && v == 4 }
	usesPipeline := func(r *atomicReq) bool { v, ok := r.value(tPrimary, pColorPipe); return ok && v != 0 }
	t.Run("the hint is refused, the pipeline is accepted", func(t *testing.T) {
		o, c := setup(t, func(r *atomicReq, flags uint32) error {
			if hasHint(r) {
				return unix.EINVAL
			}
			return nil
		})
		ok, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ)
		if !ok || err != nil {
			t.Fatalf("ok=%v err=%v reason %q", ok, err, o.reason)
		}
		if o.contentProp != 0 {
			t.Fatal("the content hint was not disabled")
		}
		if _, known := o.primary.verdict(fourccXRGB, false); known || o.reason == "color_refused" {
			t.Fatal("the pipeline was blamed for the hint")
		}
		if !o.primary.colorKnown || o.primary.colorApplied != colorSDRToPQ {
			t.Fatal("the retried frame did not apply the pipeline")
		}
	})
	t.Run("the pipeline is refused, the hint is accepted", func(t *testing.T) {
		o, c := setup(t, func(r *atomicReq, flags uint32) error {
			if usesPipeline(r) && flags&atomicTestOnly == 0 {
				return unix.EINVAL
			}
			return nil
		})
		ok, err := o.commitScanoutRect(80, c, pendingFrame{}, fullPlaneRect(200, 100), colorSDRToPQ)
		if ok || err != nil || o.reason != "color_refused" {
			t.Fatalf("ok=%v err=%v reason %q", ok, err, o.reason)
		}
		if v, known := o.primary.verdict(fourccXRGB, false); !known || v {
			t.Fatal("the pipeline refusal was not cached")
		}
		if o.contentProp != pContent {
			t.Fatal("the content hint was disabled for the pipeline's refusal")
		}
	})
}
