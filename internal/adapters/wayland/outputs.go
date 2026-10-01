package wayland

import (
	"math"
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/xdgoutput"
	"github.com/bnema/purego-libwayland/server"
)

// setOutputHDR records confirmed DRM state independently of the core layout.
// Until the first modeset report for an output it is treated as SDR.
func (s *Server) setOutputHDR(f ports.OutputFormats) {
	if s.hdrOutputs == nil {
		s.hdrOutputs = make(map[string]ports.OutputHDR)
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
	place      ports.OutputPlacement
	global     *server.Global
	resources  []*wayland.Output
	xdgOutputs []*xdgoutput.ZxdgOutputV1
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
	r.SendGeometry(int32(o.place.X), int32(o.place.Y), int32(i.PhysicalW), int32(i.PhysicalH), 0, make, model, 0)
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
	x.SendLogicalPosition(int32(o.place.X), int32(o.place.Y))
	x.SendLogicalSize(int32(o.place.Width), int32(o.place.Height))
	if withName && x.Version() >= 2 {
		x.SendName(o.name())
		x.SendDescription(o.description())
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
func (s *Server) addOutput(p ports.OutputPlacement) {
	o := &output{place: p}
	g, err := s.display.AddGlobal(wayland.OutputInterface, 4, func(c server.Client, v, id uint32) {
		r, err := wayland.NewOutput(c, int32(v), id, outputHandler{})
		if err != nil {
			return
		}
		// A client may bind a global just removed: it gets the last state
		// and no updates.
		o.resources = append(o.resources, r)
		r.OnDestroy = func() { o.resources = removeItem(o.resources, r) }
		o.sendAll(r)
		for _, m := range s.workspaceManagers {
			if m.outputBound(o.name()) {
				m.res.SendDone()
			}
		}
		s.refreshToplevels()
	})
	if err != nil {
		s.log.Warn().Err(err).Str("output", p.Info.Name).Msg("add wl_output")
		return
	}
	o.global = g
	s.outputs = append(s.outputs, o)
	s.log.Info().Str("output", p.Info.Name).Int("x", p.X).Int("w", p.Width).Int("h", p.Height).Float64("scale", p.Scale).Msg("output added")
}

// setOutputs applies the global layout from core: new outputs are
// advertised, gone ones withdrawn (their layer surfaces are closed), and
// changed ones resent.
func (s *Server) setOutputs(c ports.SetOutputs) {
	changed := !slices.Equal(s.outputPlaces, c.Outputs)
	s.outputPlaces = append(ports.Layout(nil), c.Outputs...)
	defer func() {
		if changed {
			s.refreshOutputManagers()
		}
	}()
	s.focusedOutput = c.Focused
	want := map[string]ports.OutputPlacement{}
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
		if o.place == p {
			continue
		}
		scaleChanged := o.place.Scale != p.Scale
		logicalChanged := o.place.Width != p.Width || o.place.Height != p.Height
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
		for _, r := range o.resources {
			o.sendAll(r)
		}
		for _, x := range o.xdgOutputs {
			o.sendXDG(x, false)
			if x.Version() < 3 {
				x.SendDone()
			}
		}
		if scaleChanged {
			s.log.Info().Str("output", p.Info.Name).Float64("scale", p.Scale).Int("w", p.Width).Int("h", p.Height).Msg("output scale")
		}
	}
	// Surfaces follow the scale of their output.
	for _, surf := range s.surfaces {
		surf.sendScale()
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

func registerXDGOutput(d *server.Display, s *Server) error {
	return xdgoutput.NewZxdgOutputManagerV1Global(d, 3, func(c server.Client, v, id uint32) {
		_, _ = xdgoutput.NewZxdgOutputManagerV1(c, int32(v), id, xdgOutputManager{s})
	})
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
