package wayland

import (
	"math"
	"slices"
	"strings"

	"bnema.dev/purego-libwayland/server"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/go-wayland-bindings/server/xdgoutput"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// setOutputHDR records confirmed DRM state independently of the core layout.
// Until the first modeset report for an output it is treated as SDR.
func (s *Server) setOutputHDR(f domain.OutputFormats) {
	if s.hdrOutputs == nil {
		s.hdrOutputs = make(map[string]domain.OutputHDR)
	}
	old, had := s.hdrOutputs[f.Output]
	if f.HDR == nil {
		delete(s.hdrOutputs, f.Output)
	} else {
		s.hdrOutputs[f.Output] = *f.HDR
	}
	if (f.HDR == nil && had) || (f.HDR != nil && (!had || old != *f.HDR)) {
		s.colorOutputChanged(f.Output)
	}
	if (f.HDR != nil) != had {
		s.castsHDRChanged(f.Output)
	}
}

// Outputs. Core sends the global layout (ports.SetOutputs); each output is
// one wl_output global, removed when the display is unplugged. Every size
// sent to clients (configures, xdg-output, layer configures, pointer
// coordinates) is logical. Clients that bind wp_fractional_scale_v1 get the
// exact scale of their output in 1/120 steps and draw at physical size
// through wp_viewporter; others get wl_output.scale rounded up and the
// renderer downscales.

// output is one advertised display and the resources clients bound to it.
type output struct {
	place domain.OutputPlacement
	// Public protocol origin only; place remains in compositor layout space.
	originX, originY int
	global           *server.Global
	xglobal          *server.Global
	xgeneration      *outputGeneration
	xresources       []*wayland.Output
	resources        []*wayland.Output
	xdgOutputs       []*xdgoutput.ZxdgOutputV1
	// xdgOutputsX are the private xdg-outputs of the Xwayland client (X space).
	xdgOutputsX []*xdgoutput.ZxdgOutputV1
}

func (o *output) name() string { return o.place.Info.Name }

// integerScale is wl_output.scale: the fractional scale rounded up.
func integerScale(scale float64) int32 { return int32(math.Ceil(max(scale, 1) - 1e-9)) }

func (o *output) description() string {
	i := o.place.Info
	return strings.Join(strings.Fields(i.Make+" "+i.Model+" "+i.Serial+" ("+i.Name+")"), " ")
}

// sendAll sends the full wl_output state, then done (v2+).
func (o *output) sendAll(r *wayland.Output) {
	i := o.place.Info
	make, model := i.Make, i.Model
	if make == "" {
		make, model = "neferwl", "headless"
	}
	refresh := i.RefreshMilli
	if refresh == 0 {
		refresh = 60000
	}
	r.SendGeometry(int32(o.place.X-o.originX), int32(o.place.Y-o.originY), int32(i.PhysicalW), int32(i.PhysicalH), 0, make, model, int32(o.place.Transform))
	// The mode stays physical; the scale tells clients how to divide it.
	r.SendMode(3, int32(i.Width), int32(i.Height), int32(refresh))
	if r.Version() >= 2 {
		r.SendScale(integerScale(o.place.Scale))
	}
	if r.Version() >= 4 {
		r.SendName(i.Name)
		r.SendDescription(o.description())
	}
	if r.Version() >= 2 {
		r.SendDone()
	}
}

func (o *output) sendXDG(x *xdgoutput.ZxdgOutputV1, withName bool) {
	x.SendLogicalPosition(int32(o.place.X-o.originX), int32(o.place.Y-o.originY))
	x.SendLogicalSize(int32(o.place.Width), int32(o.place.Height))
	if withName && x.Version() >= 2 {
		x.SendName(o.name())
		x.SendDescription(o.description())
	}
}

// sendXDGX sends the private xdg-output state: the layout position moved to
// X space and the logical size.
func (o *output) sendXDGX(s *Server, x *xdgoutput.ZxdgOutputV1, withName bool) {
	if !s.xspace.Includes(o.place) {
		return
	}
	ox, oy := s.xspace.Origin()
	x.SendLogicalPosition(int32(o.place.X-ox), int32(o.place.Y-oy))
	x.SendLogicalSize(int32(o.place.Width), int32(o.place.Height))
	if withName && x.Version() >= 2 {
		x.SendName(o.name())
		x.SendDescription(o.description())
	}
}

// republishXDGX resends every private xdg-output after a layout change, so
// Xwayland resizes the root and its RandR outputs.
func (s *Server) republishXDGX() {
	for _, o := range s.outputs {
		for _, x := range o.xdgOutputsX {
			o.sendXDGX(s, x, false)
			if x.Version() < 3 {
				x.SendDone()
			}
		}
		for _, r := range o.resources {
			if r.Client() == s.xwayland.client && r.Version() >= 2 {
				r.SendDone()
			}
		}
	}
}

// outputOf returns the output a wl_output resource belongs to, or nil for a
// removed one.
func (s *Server) outputOf(r *wayland.Output) *output {
	if r == nil {
		return nil
	}
	for _, o := range s.outputs {
		for _, v := range o.resources {
			if v.Resource == r.Resource {
				return o
			}
		}
	}
	return nil
}

// outputByName returns the output with that connector, else the focused one,
// else the first; nil when there is none.
func (s *Server) outputByName(name string) *output {
	var first, focused *output
	for _, o := range s.outputs {
		if o.name() == name {
			return o
		}
		if first == nil {
			first = o
		}
		if o.name() == s.focusedOutput {
			focused = o
		}
	}
	if focused != nil {
		return focused
	}
	return first
}

// addOutput advertises a new wl_output global.
func (s *Server) addOutput(p domain.OutputPlacement) {
	ox, oy := s.publicOutputOrigin()
	o := &output{place: p, originX: ox, originY: oy}
	s.outputs = append(s.outputs, o)
	o.global = s.outputGlobal(o, false)
	if o.global == nil {
		s.outputs = removeItem(s.outputs, o)
		return
	}
	s.syncXOutput(o)
	s.log.Info().Str("output", p.Info.Name).Int("x", p.X).Int("y", p.Y).Int("w", p.Width).Int("h", p.Height).Float64("scale", p.Scale).Msg("output added")
}

// publicOutputOrigin translates client-visible coordinates without moving the
// compositor layout. Core has already selected the configured primary output.
func (s *Server) publicOutputOrigin() (int, int) {
	for _, p := range s.outputPlaces {
		if p.Primary {
			return p.X, p.Y
		}
	}
	return 0, 0
}

// setOutputs applies the global layout from core: new outputs are
// advertised, gone ones withdrawn (their layer surfaces are closed), and
// changed ones resent.
func (s *Server) setOutputs(c ports.SetOutputs) {
	changed := !slices.EqualFunc(s.outputPlaces, c.Outputs, domain.OutputPlacement.Equal)
	focusChanged := s.focusedOutput != c.Focused
	s.outputPlaces = append(domain.Layout(nil), c.Outputs...)
	xChanged := s.xspace.Set(s.outputPlaces)
	if changed {
		for _, p := range s.outputPlaces {
			if !s.xspace.Includes(p) {
				s.log.Warn().Str("output", p.Info.Name).Msg("output excluded from X11: rectangle exceeds X-space coordinate or output-count limit")
			}
		}
	}
	if s.xwm != nil {
		s.xwm.LayoutChanged(xChanged)
	}
	defer func() {
		if changed {
			s.refreshOutputManagers()
		}
		if changed || focusChanged {
			s.castInventoryChanged()
		}
	}()
	s.focusedOutput = c.Focused
	want := map[string]domain.OutputPlacement{}
	for _, p := range c.Outputs {
		want[p.Info.Name] = p
	}
	kept := s.outputs[:0]
	for _, o := range s.outputs {
		if _, ok := want[o.name()]; ok {
			kept = append(kept, o)
			continue
		}
		o.global.Remove()
		if o.xglobal != nil {
			o.xgeneration.live = false
			s.retirePrivateOutput(o.xglobal)
			o.xglobal.Remove()
			o.xglobal = nil
		}
		delete(s.hdrOutputs, o.name())
		delete(s.colorIdentity, o.name())
		s.outputGone(o)
		s.log.Info().Str("output", o.name()).Msg("output removed")
		for _, l := range s.layers {
			if l.output == o {
				l.close()
			}
		}
		for _, l := range s.lockSurfaces {
			if l.output == o {
				l.unmap()
			}
		}
		for _, surf := range s.surfaces {
			surf.leave(o)
		}
	}
	s.outputs = kept
	// Windows of a removed output leave it until core moves them.
	s.refreshToplevels()
	for _, p := range c.Outputs {
		o := s.outputByNameExact(p.Info.Name)
		if o == nil {
			s.addOutput(p)
			continue
		}
		if o.place.Equal(p) {
			continue
		}
		// A 90° to 270° change keeps the logical size: lock surfaces are
		// logical-sized and need no reconfigure then.
		logicalChanged := o.place.Width != p.Width || o.place.Height != p.Height
		// The first layout from core moves an output off the placement it was
		// added with, so this line carries the real position.
		s.log.Info().Str("output", p.Info.Name).Int("x", p.X).Int("y", p.Y).Int("w", p.Width).Int("h", p.Height).Float64("scale", p.Scale).Int("transform", int(p.Transform)).Bool("primary", p.Primary).Msg("output changed")
		o.place = p
		if logicalChanged {
			for _, l := range s.lockSurfaces {
				if l.output == o && !l.closed {
					l.sendConfigure()
				}
			}
			s.lockSurfaceChanged()
		}
		// Buffer sizes follow the mode and the scale (region and workspace
		// sources too): sessions send new constraints when theirs changed.
		s.refreshSessions()
	}
	if changed {
		for _, o := range s.outputs {
			s.syncXOutput(o)
		}
		ox, oy := s.publicOutputOrigin()
		for _, o := range s.outputs {
			o.originX, o.originY = ox, oy
			// xdg-output v3 is committed by wl_output.done, so send its
			// position before sendAll's done, including on origin-only changes.
			for _, x := range o.xdgOutputs {
				o.sendXDG(x, false)
				if x.Version() < 3 {
					x.SendDone()
				}
			}
			for _, r := range o.resources {
				o.sendAll(r)
			}
		}
	}
	// Surfaces follow the scale of their output.
	for _, surf := range s.surfaces {
		surf.sendScale()
	}
	if changed {
		s.republishXDGX()
	}
	s.notifyColorFeedback()
	// Layer surfaces sized from the output follow its new logical size.
	adopted := false
	for _, l := range s.layers {
		if l.output == nil && !l.closed {
			// Created before any output: it takes the focused one.
			if l.output = s.outputByName(""); l.output != nil && l.configured {
				l.sendConfigure()
				adopted = adopted || l.mapped
				continue
			}
		}
		// Uses committed state only: pending requests wait for the client's commit.
		if l.configured && (l.current.width == 0 || l.current.height == 0) {
			l.sendConfigure()
		}
	}
	if adopted {
		s.layerChanged()
	}
}

func (s *Server) outputByNameExact(name string) *output {
	for _, o := range s.outputs {
		if o.name() == name {
			return o
		}
	}
	return nil
}

type outputHandler struct{}

func (outputHandler) Release(*wayland.Output) {}

// registerXDGOutput registers the public zxdg_output_manager_v1 and a second,
// private one for Xwayland whose outputs are in X space (see globalVisible).
func registerXDGOutput(d *server.Display, s *Server) error {
	var err error
	s.xdgPublic, err = d.AddGlobal(xdgoutput.ZxdgOutputManagerV1Interface, 3, func(c server.Client, v, id uint32) {
		_, _ = xdgoutput.NewZxdgOutputManagerV1(c, int32(v), id, xdgOutputManager{s})
	})
	if err != nil {
		return err
	}
	s.xdgPrivate, err = d.AddGlobal(xdgoutput.ZxdgOutputManagerV1Interface, 3, func(c server.Client, v, id uint32) {
		_, _ = xdgoutput.NewZxdgOutputManagerV1(c, int32(v), id, xdgOutputManagerX{s})
	})
	return err
}

type xdgOutputManagerX struct{ server *Server }

func (xdgOutputManagerX) Destroy(*xdgoutput.ZxdgOutputManagerV1) {}
func (m xdgOutputManagerX) GetXdgOutput(r *xdgoutput.ZxdgOutputManagerV1, id uint32, wl *wayland.Output) {
	x, err := xdgoutput.NewZxdgOutputV1(r.Client(), r.Version(), id, xdgOutput{})
	if err != nil {
		return
	}
	o := m.server.outputOf(wl)
	if o == nil {
		return
	}
	o.xdgOutputsX = append(o.xdgOutputsX, x)
	x.OnDestroy = func() { o.xdgOutputsX = removeItem(o.xdgOutputsX, x) }
	o.sendXDGX(m.server, x, true)
	if r.Version() < 3 || wl.Version() < 2 {
		x.SendDone()
	} else {
		wl.SendDone()
	}
}

type xdgOutputManager struct{ server *Server }

func (xdgOutputManager) Destroy(*xdgoutput.ZxdgOutputManagerV1) {}
func (m xdgOutputManager) GetXdgOutput(r *xdgoutput.ZxdgOutputManagerV1, id uint32, wl *wayland.Output) {
	x, err := xdgoutput.NewZxdgOutputV1(r.Client(), r.Version(), id, xdgOutput{})
	if err != nil {
		return
	}
	o := m.server.outputOf(wl)
	if o == nil {
		// Removed output: the object stays inert.
		return
	}
	o.xdgOutputs = append(o.xdgOutputs, x)
	x.OnDestroy = func() { o.xdgOutputs = removeItem(o.xdgOutputs, x) }
	o.sendXDG(x, true)
	if r.Version() < 3 || wl.Version() < 2 {
		x.SendDone()
	} else {
		wl.SendDone()
	}
}

type xdgOutput struct{}

func (xdgOutput) Destroy(*xdgoutput.ZxdgOutputV1) {}

type outputGeneration struct{ live bool }

// outputGlobal separates native and Xwayland registry lifetimes. A withdrawn
// X global leaves its already-bound resources inert until the client releases them.
func (s *Server) outputGlobal(o *output, private bool) *server.Global {
	// libwayland invokes the visibility filter synchronously while AddGlobal
	// broadcasts, before the returned pointer can be assigned to the output.
	generation := &outputGeneration{live: true}
	if private {
		o.xgeneration = generation
	}
	s.creatingPrivateOutput = private
	defer func() { s.creatingPrivateOutput = false }()
	g, err := s.display.AddGlobal(wayland.OutputInterface, 4, func(c server.Client, v, id uint32) {
		r, err := wayland.NewOutput(c, int32(v), id, outputHandler{})
		if err != nil {
			return
		}
		if !generation.live {
			return
		}
		o.resources = append(o.resources, r)
		if private {
			o.xresources = append(o.xresources, r)
			r.OnDestroy = func() { o.xresources = removeItem(o.xresources, r); o.resources = removeItem(o.resources, r) }
		} else {
			r.OnDestroy = func() { o.resources = removeItem(o.resources, r) }
		}
		o.sendAll(r)
		if private {
			for _, surf := range s.surfaces {
				if surf.on == o && !surf.destroyed && surf.wl.Resource.Alive() && surf.wl.Client() == c {
					surf.wl.SendEnter(r)
				}
			}
		}
		for _, m := range s.workspaceManagers {
			if m.outputBound(o.name()) {
				m.res.SendDone()
			}
		}
		s.refreshToplevels()
	})
	if err != nil {
		s.log.Warn().Err(err).Str("output", o.name()).Msg("add wl_output")
		return nil
	}
	return g
}

func (s *Server) syncXOutput(o *output) {
	included := s.xspace.Includes(o.place)
	if !included && o.xglobal != nil {
		o.xgeneration.live = false
		s.retirePrivateOutput(o.xglobal)
		for _, surf := range s.surfaces {
			if surf.on != o || surf.destroyed || !surf.wl.Resource.Alive() {
				continue
			}
			for _, r := range o.xresources {
				if r.Resource.Alive() && r.Client() == surf.wl.Client() {
					surf.wl.SendLeave(r)
				}
			}
		}
		o.xglobal.Remove()
		o.xglobal = nil
		// Resources from the withdrawn generation must never receive new geometry.
		for _, r := range o.xresources {
			o.resources = removeItem(o.resources, r)
		}
		o.xresources = nil
		o.xdgOutputsX = nil
	}
	if included && o.xglobal == nil {
		o.xglobal = s.outputGlobal(o, true)
	}
}

// retirePrivateOutput retains visibility classification independently of the
// active output list. Global.Remove keeps the bind callback alive until display
// destruction, when Server.Run clears this display-owned set.
func (s *Server) retirePrivateOutput(g *server.Global) {
	if s.retiredPrivateOutputs == nil {
		s.retiredPrivateOutputs = make(map[*server.Global]struct{})
	}
	s.retiredPrivateOutputs[g] = struct{}{}
}
