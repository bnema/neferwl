package wayland

import (
	"math"

	cm "github.com/bnema/purego-libwayland/protocol/colormanagement"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// SurfaceColor is the committed color encoding of a client surface. Set is false
// when no explicit description was committed; the default is compositor-defined.
type SurfaceColor struct {
	Set                            bool
	TF, Primaries, MaxCLL, MaxFALL uint32
}

func (s *surface) ColorDescription() SurfaceColor { return s.color }

type colorDescription struct {
	SurfaceColor
	min, max, white, targetMin, targetMax uint32
	cllSet, fallSet                       bool
	identity                              uint64
}

const (
	srgb      = uint32(cm.WpColorManagerV1PrimariesSrgb)
	bt2020    = uint32(cm.WpColorManagerV1PrimariesBt2020)
	gamma22   = uint32(cm.WpColorManagerV1TransferFunctionGamma22)
	pq        = uint32(cm.WpColorManagerV1TransferFunctionSt2084Pq)
	extLinear = uint32(cm.WpColorManagerV1TransferFunctionExtLinear)
)

func (s *Server) nextColorID() uint64 { s.colorID++; return s.colorID }
func (s *Server) outputColor(o *output) colorDescription {
	d := colorDescription{SurfaceColor: SurfaceColor{Set: true, TF: uint32(cm.WpColorManagerV1TransferFunctionSrgb), Primaries: srgb}, min: 2000, max: 80, white: 80, targetMin: 2000, targetMax: 80}
	if o != nil {
		if h, ok := s.hdrOutputs[o.name()]; ok {
			d.TF, d.Primaries, d.white = pq, bt2020, 203
			d.min = uint32(math.Max(0, math.Round(h.MinLuminance*10000)))
			d.max = d.min/10000 + 10000 // PQ primary volume is fixed by ST 2084.
			d.targetMin, d.targetMax = d.min, uint32(math.Max(1, math.Round(h.MaxLuminance)))
			d.MaxCLL = d.targetMax
			d.MaxFALL = uint32(math.Max(0, math.Round(h.MaxFrameAverage)))
			d.cllSet, d.fallSet = true, true
		}
	}
	// Identity changes only when the description changes, not on each query.
	if o != nil {
		if s.colorIdentity == nil {
			s.colorIdentity = map[string]uint64{}
		}
		d.identity = s.colorIdentity[o.name()]
		if d.identity == 0 {
			d.identity = s.nextColorID()
			s.colorIdentity[o.name()] = d.identity
		}
	}
	return d
}
func registerColorManagement(d *server.Display, s *Server) error {
	return cm.NewWpColorManagerV1Global(d, 2, func(c server.Client, v, id uint32) {
		r, err := cm.NewWpColorManagerV1(c, int32(v), id, colorManager{s})
		if err != nil {
			return
		}
		r.SendSupportedIntent(uint32(cm.WpColorManagerV1RenderIntentPerceptual))
		r.SendSupportedFeature(uint32(cm.WpColorManagerV1FeatureParametric))
		for _, tf := range []uint32{pq, extLinear, uint32(cm.WpColorManagerV1TransferFunctionSrgb)} {
			r.SendSupportedTfNamed(tf)
		}

		for _, p := range []uint32{srgb, bt2020} {
			r.SendSupportedPrimariesNamed(p)
		}
		r.SendDone()
	})
}

type colorManager struct{ s *Server }

func (colorManager) Destroy(*cm.WpColorManagerV1) {}
func (m colorManager) GetOutput(r *cm.WpColorManagerV1, id uint32, out *wayland.Output) {
	h := &colorOutput{s: m.s, o: m.s.outputOf(out)}
	res, err := cm.NewWpColorManagementOutputV1(r.Client(), r.Version(), id, h)
	if err != nil {
		return
	}
	h.res = res
	m.s.colorOutputs[h] = struct{}{}
	res.OnDestroy = func() { delete(m.s.colorOutputs, h) }
}
func (m colorManager) GetSurface(r *cm.WpColorManagerV1, id uint32, wl *wayland.Surface) {
	var surf *surface
	if wl != nil {
		surf = m.s.surfaces[wl.Resource]
	}
	if surf != nil && surf.colorControl != nil {
		r.PostError(uint32(cm.WpColorManagerV1ErrorSurfaceExists), "surface already has color management")
		return
	}
	h := &colorSurface{surf: surf}
	res, err := cm.NewWpColorManagementSurfaceV1(r.Client(), r.Version(), id, h)
	if err != nil {
		return
	}
	if surf != nil {
		surf.colorControl = h
		res.OnDestroy = func() {
			if surf.colorControl == h {
				surf.colorControl = nil
				surf.next.color = SurfaceColor{}
			}
		}
	}
}
func (m colorManager) GetSurfaceFeedback(r *cm.WpColorManagerV1, id uint32, wl *wayland.Surface) {
	var surf *surface
	if wl != nil {
		surf = m.s.surfaces[wl.Resource]
	}
	h := &colorFeedback{s: m.s, surf: surf}
	res, err := cm.NewWpColorManagementSurfaceFeedbackV1(r.Client(), r.Version(), id, h)
	if err != nil {
		return
	}
	h.res = res
	m.s.colorFeedbacks[h] = struct{}{}
	res.OnDestroy = func() { delete(m.s.colorFeedbacks, h) }
}
func (colorManager) CreateIccCreator(r *cm.WpColorManagerV1, _ uint32) {
	r.PostError(uint32(cm.WpColorManagerV1ErrorUnsupportedFeature), "ICC unsupported")
}
func (m colorManager) CreateParametricCreator(r *cm.WpColorManagerV1, id uint32) {
	_, _ = cm.NewWpImageDescriptionCreatorParamsV1(r.Client(), r.Version(), id, &colorParams{s: m.s})
}
func (colorManager) CreateWindowsScrgb(r *cm.WpColorManagerV1, _ uint32) {
	r.PostError(uint32(cm.WpColorManagerV1ErrorUnsupportedFeature), "scRGB unsupported")
}
func (colorManager) CreateWindowsBt2100(r *cm.WpColorManagerV1, _ uint32) {
	r.PostError(uint32(cm.WpColorManagerV1ErrorUnsupportedFeature), "BT2100 unsupported")
}
func (colorManager) GetImageDescription(r *cm.WpColorManagerV1, _ uint32, _ *cm.WpImageDescriptionReferenceV1) {
	r.PostError(uint32(cm.WpColorManagerV1ErrorUnsupportedFeature), "references unsupported")
}

type colorOutput struct {
	s   *Server
	o   *output
	res *cm.WpColorManagementOutputV1
}

func (*colorOutput) Destroy(*cm.WpColorManagementOutputV1) {}
func (h *colorOutput) GetImageDescription(r *cm.WpColorManagementOutputV1, id uint32) {
	if h.o == nil || h.s.outputByNameExact(h.o.name()) != h.o {
		h.s.newColorDescription(r.Client(), r.Version(), id, colorDescription{}, uint32(cm.WpImageDescriptionV1CauseNoOutput))
		return
	}
	h.s.newColorDescription(r.Client(), r.Version(), id, h.s.outputColor(h.o), 0)
}

type colorSurface struct{ surf *surface }

func (h *colorSurface) Destroy(*cm.WpColorManagementSurfaceV1) {
	if h.surf != nil && !h.surf.destroyed {
		h.surf.next.color = SurfaceColor{}
	}
}
func (h *colorSurface) SetImageDescription(r *cm.WpColorManagementSurfaceV1, desc *cm.WpImageDescriptionV1, intent uint32) {
	if h.surf == nil || h.surf.destroyed {
		r.PostError(uint32(cm.WpColorManagementSurfaceV1ErrorInert), "surface destroyed")
		return
	}
	if intent != 0 {
		r.PostError(uint32(cm.WpColorManagementSurfaceV1ErrorRenderIntent), "unsupported intent")
		return
	}
	if desc == nil {
		r.PostError(uint32(cm.WpColorManagementSurfaceV1ErrorImageDescription), "missing description")
		return
	}
	d, ok := h.surf.server.colorDescriptions[desc.Resource]
	if !ok {
		r.PostError(uint32(cm.WpColorManagementSurfaceV1ErrorImageDescription), "description not ready")
		return
	}
	h.surf.next.color = d.SurfaceColor
	h.surf.server.log.Debug().Str("component", "wayland").Uint32("tf", d.TF).Uint32("primaries", d.Primaries).Msg("surface color description set")
}
func (h *colorSurface) UnsetImageDescription(r *cm.WpColorManagementSurfaceV1) {
	if h.surf == nil || h.surf.destroyed {
		r.PostError(uint32(cm.WpColorManagementSurfaceV1ErrorInert), "surface destroyed")
		return
	}
	h.surf.next.color = SurfaceColor{}
	h.surf.server.log.Debug().Str("component", "wayland").Msg("surface color description unset")
}

type colorFeedback struct {
	s    *Server
	surf *surface
	res  *cm.WpColorManagementSurfaceFeedbackV1
	last *output
}

func (*colorFeedback) Destroy(*cm.WpColorManagementSurfaceFeedbackV1) {}
func (h *colorFeedback) preferred(r *cm.WpColorManagementSurfaceFeedbackV1, id uint32) {
	if h.surf == nil || h.surf.destroyed {
		r.PostError(uint32(cm.WpColorManagementSurfaceFeedbackV1ErrorInert), "surface destroyed")
		return
	}
	h.s.newColorDescription(r.Client(), r.Version(), id, h.s.outputColor(h.s.outputOfSurface(h.surf)), 0)
}
func (h *colorFeedback) GetPreferred(r *cm.WpColorManagementSurfaceFeedbackV1, id uint32) {
	h.preferred(r, id)
}
func (h *colorFeedback) GetPreferredParametric(r *cm.WpColorManagementSurfaceFeedbackV1, id uint32) {
	h.preferred(r, id)
}
func (s *Server) notifyColorFeedback() {
	for h := range s.colorFeedbacks {
		if h.surf == nil || h.surf.destroyed {
			continue
		}
		o := s.outputOfSurface(h.surf)
		if o != h.last {
			h.last = o
			h.changed(s.outputColor(o).identity)
		}
	}
}
func (h *colorFeedback) changed(id uint64) {
	if h.res.Version() >= 2 {
		h.res.SendPreferredChanged2(uint32(id>>32), uint32(id))
	} else {
		h.res.SendPreferredChanged(uint32(id))
	}
}
func (s *Server) colorOutputChanged(name string) {
	if s.outputByNameExact(name) == nil {
		return
	}
	if s.colorIdentity == nil {
		s.colorIdentity = map[string]uint64{}
	}
	s.colorIdentity[name] = s.nextColorID()
	for h := range s.colorOutputs {
		if h.o != nil && h.o.name() == name && s.outputByNameExact(name) == h.o {
			h.res.SendImageDescriptionChanged()
			for _, r := range h.o.resources {
				if r.Client() == h.res.Client() && r.Version() >= 2 {
					r.SendDone()
				}
			}
		}
	}
	for h := range s.colorFeedbacks {
		if h.surf != nil && !h.surf.destroyed {
			if o := s.outputOfSurface(h.surf); o != nil && o.name() == name {
				h.changed(s.colorIdentity[name])
			}
		}
	}
}
func (s *Server) newColorDescription(c server.Client, v int32, id uint32, d colorDescription, failure uint32) {
	s.createColorDescription(c, v, id, d, failure, true)
}
func (s *Server) createColorDescription(c server.Client, v int32, id uint32, d colorDescription, failure uint32, information bool) {
	h := &colorImage{s: s, d: d, ready: failure == 0, information: information}
	res, err := cm.NewWpImageDescriptionV1(c, v, id, h)
	if err != nil {
		return
	}
	if failure != 0 {
		msg := "output unavailable"
		if failure == uint32(cm.WpImageDescriptionV1CauseUnsupported) {
			msg = "unsupported transfer function and primaries combination"
		}
		res.SendFailed(failure, msg)
		return
	}
	if d.identity == 0 {
		d.identity = s.nextColorID()
		h.d.identity = d.identity
	}
	s.colorDescriptions[res.Resource] = d
	res.OnDestroy = func() { delete(s.colorDescriptions, res.Resource) }
	if v >= 2 {
		res.SendReady2(uint32(d.identity>>32), uint32(d.identity))
	} else {
		res.SendReady(uint32(d.identity))
	}
}

type colorImage struct {
	s           *Server
	d           colorDescription
	ready       bool
	information bool
}

func (*colorImage) Destroy(*cm.WpImageDescriptionV1) {}
func (h *colorImage) GetInformation(r *cm.WpImageDescriptionV1, id uint32) {
	if !h.ready {
		r.PostError(uint32(cm.WpImageDescriptionV1ErrorNotReady), "description not ready")
		return
	}
	if !h.information {
		r.PostError(uint32(cm.WpImageDescriptionV1ErrorNoInformation), "information not allowed")
		return
	}
	info, err := cm.NewWpImageDescriptionInfoV1(r.Client(), r.Version(), id, colorInfo{})
	if err != nil {
		return
	}
	d := h.d
	coords := srgbCoords
	if d.Primaries == bt2020 {
		coords = bt2020Coords
	}
	info.SendPrimaries(coords[0], coords[1], coords[2], coords[3], coords[4], coords[5], coords[6], coords[7])
	info.SendPrimariesNamed(d.Primaries)
	info.SendTfNamed(d.TF)
	if d.min != 0 || d.max != 0 {
		info.SendLuminances(d.min, d.max, d.white)
	}
	info.SendTargetPrimaries(coords[0], coords[1], coords[2], coords[3], coords[4], coords[5], coords[6], coords[7])
	info.SendTargetLuminance(d.targetMin, d.targetMax)
	if d.cllSet {
		info.SendTargetMaxCll(d.MaxCLL)
	}
	if d.fallSet {
		info.SendTargetMaxFall(d.MaxFALL)
	}
	info.SendDone()
	info.Destroy()
}

type colorInfo struct{}

func (colorInfo) Destroy(*cm.WpImageDescriptionInfoV1) {}

var srgbCoords = [8]int32{640000, 330000, 300000, 600000, 150000, 60000, 312700, 329000}
var bt2020Coords = [8]int32{708000, 292000, 170000, 797000, 131000, 46000, 312700, 329000}

type colorParams struct {
	s              *Server
	d              colorDescription
	tfSet, primSet bool
}

func (h *colorParams) fail(r *cm.WpImageDescriptionCreatorParamsV1, code cm.WpImageDescriptionCreatorParamsV1Error, msg string) {
	r.PostError(uint32(code), msg)
}
func (h *colorParams) Create(r *cm.WpImageDescriptionCreatorParamsV1, id uint32) {
	if !h.tfSet || !h.primSet {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorIncompleteSet, "transfer and primaries required")
		return
	}
	if h.d.cllSet && h.d.fallSet && h.d.MaxFALL > h.d.MaxCLL {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "MaxFALL exceeds MaxCLL")
		return
	}
	if r.Version() == 1 {
		for _, x := range []struct {
			set   bool
			value uint32
		}{{h.d.cllSet, h.d.MaxCLL}, {h.d.fallSet, h.d.MaxFALL}} {
			if x.set && (uint64(x.value)*10000 <= uint64(h.d.targetMin) || x.value > h.d.targetMax) {
				h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorInvalidLuminance, "content luminance outside mastering range")
				return
			}
		}
	}
	// Advertised transfer functions and primaries do not imply that every
	// cross-product can be rendered. Reject unsupported combinations as an
	// image-description failure, not a protocol error.
	validSDR := h.d.Primaries == srgb && (h.d.TF == extLinear || h.d.TF == uint32(cm.WpColorManagerV1TransferFunctionSrgb))
	if !validSDR && !(h.d.Primaries == bt2020 && h.d.TF == pq) {
		h.s.createColorDescription(r.Client(), r.Version(), id, h.d, uint32(cm.WpImageDescriptionV1CauseUnsupported), false)
		return
	}
	h.s.createColorDescription(r.Client(), r.Version(), id, h.d, 0, false)
}
func (h *colorParams) SetTfNamed(r *cm.WpImageDescriptionCreatorParamsV1, tf uint32) {
	if h.tfSet {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorAlreadySet, "TF already set")
		return
	}
	if tf == gamma22 {
		h.tfSet = true
		h.d.TF = tf
		h.d.Set = true
		return // Create reports failed(unsupported) for unsupported named TFs.
	}
	if tf != pq && tf != extLinear && tf != uint32(cm.WpColorManagerV1TransferFunctionSrgb) {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorInvalidTf, "unsupported TF")
		return
	}
	h.tfSet = true
	h.d.TF = tf
	h.d.Set = true
	if tf == pq {
		h.d.min, h.d.max, h.d.white = 50, 10000, 203
	} else if tf == extLinear {
		// Protocol defaults: sRGB primary volume, 80 cd/m² reference white.
		h.d.min, h.d.max, h.d.white = 2000, 80, 80
	} else {
		h.d.min, h.d.max, h.d.white = 100, 100, 100
	}
	h.d.targetMin, h.d.targetMax = h.d.min, h.d.max
}
func (h *colorParams) SetTfPower(r *cm.WpImageDescriptionCreatorParamsV1, _ uint32) {
	h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorUnsupportedFeature, "TF power unsupported")
}
func (h *colorParams) SetPrimariesNamed(r *cm.WpImageDescriptionCreatorParamsV1, p uint32) {
	if h.primSet {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorAlreadySet, "primaries already set")
		return
	}
	if p != srgb && p != bt2020 {
		h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorInvalidPrimariesNamed, "unsupported primaries")
		return
	}
	h.primSet = true
	h.d.Primaries = p
}
func (h *colorParams) SetPrimaries(r *cm.WpImageDescriptionCreatorParamsV1, _, _, _, _, _, _, _, _ int32) {
	h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorUnsupportedFeature, "custom primaries unsupported")
}
func (h *colorParams) SetLuminances(r *cm.WpImageDescriptionCreatorParamsV1, min, max, white uint32) {
	h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorUnsupportedFeature, "custom luminances unsupported")
}
func (h *colorParams) SetMasteringDisplayPrimaries(r *cm.WpImageDescriptionCreatorParamsV1, _, _, _, _, _, _, _, _ int32) {
	h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorUnsupportedFeature, "mastering primaries unsupported")
}
func (h *colorParams) SetMasteringLuminance(r *cm.WpImageDescriptionCreatorParamsV1, _, _ uint32) {
	h.fail(r, cm.WpImageDescriptionCreatorParamsV1ErrorUnsupportedFeature, "mastering luminance unsupported")
}
func (h *colorParams) SetMaxCll(_ *cm.WpImageDescriptionCreatorParamsV1, v uint32) {
	h.d.MaxCLL = v
	h.d.cllSet = true
}
func (h *colorParams) SetMaxFall(_ *cm.WpImageDescriptionCreatorParamsV1, v uint32) {
	h.d.MaxFALL = v
	h.d.fallSet = true
}
