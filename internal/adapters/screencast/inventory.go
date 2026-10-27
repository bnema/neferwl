package screencast

import (
	"reflect"
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerobus"
)

// applyInventory takes the outputs and windows wayland published. The
// receiver owns the slices. Departures of outputs and windows are signalled
// to the clients that watch them (the first inventory is not a change).
func (s *Service) applyInventory(inv domain.CastInventory) {
	slices.SortFunc(inv.Outputs, func(a, b domain.CastOutput) int { return strings.Compare(a.Info.Name, b.Info.Name) })
	slices.SortFunc(inv.Windows, func(a, b domain.CastWindow) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	old, had := s.inv, s.haveInv
	s.inv, s.haveInv = inv, true
	defer signal(s.inventoryApplied)
	if !had {
		return
	}
	if s.owns[1] && !reflect.DeepEqual(old.Outputs, inv.Outputs) {
		s.notifyAllowed(pathDisplayConfig, ifaceDisplayConfig, "MonitorsChanged")
	}
	if s.owns[2] && !reflect.DeepEqual(old.Windows, inv.Windows) {
		s.notifyAllowed(pathIntrospect, ifaceIntrospect, "WindowsChanged")
	}
}

// notifyAllowed sends a body-less signal to each peer that passed the caller
// check and whose executable the allowlist admits now. The signals are
// directed: the inventory (output names, window titles) is never broadcast,
// so a peer that was refused, or has not been checked, learns nothing. At
// most maxCallers peers are served, from the cache, without allocation. The
// executable is read again through the pidfd, as at the privileged entry
// points. Peers that left the bus are gone from the cache (onGone).
func (s *Service) notifyAllowed(path, iface, member string) {
	for i := range s.callers {
		c := &s.callers[i]
		if c.state != callerKnown || !s.allowed(c.exe) || !s.recheck(c) {
			continue
		}
		s.conn.NewSignalTo(c.name, path, iface, member, "")
		s.send()
	}
}

// isBuiltin follows Mutter: the connector of a laptop panel.
func isBuiltin(connector string) bool {
	for _, p := range [...]string{"eDP", "LVDS", "DSI"} {
		if strings.HasPrefix(connector, p) {
			return true
		}
	}
	return false
}

// refreshHz is a refresh rate in Hz from millihertz.
func refreshHz(milli int) float64 { return float64(milli) / 1000 }

// replyState answers DisplayConfig.GetCurrentState:
// (u, a((ssss)a(siiddada{sv})a{sv}), a(iiduba(ssss)a{sv}), a{sv}).
// layout-mode 1 is physical: logical sizes are scaled sizes of the modes.
// Exactly one mode of each monitor is current, as x-d-p-g requires.
func (s *Service) replyState(req *request) {
	e := s.replyTo(req, "ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv}")
	if e == nil {
		return
	}
	e.Uint32(0)
	monitors := e.BeginArray('(')
	for i := range s.inv.Outputs {
		o := &s.inv.Outputs[i]
		e.Struct()
		s.encodeNames(e, o)
		modes := e.BeginArray('(')
		cur := o.Current
		if cur < 0 || cur >= len(o.Modes) {
			cur = 0
		}
		for j, m := range o.Modes {
			e.Struct()
			e.Str(modeID(m))
			e.Int32(int32(m.Width))
			e.Int32(int32(m.Height))
			e.Float64(refreshHz(m.RefreshMilli))
			e.Float64(1)
			sc := e.BeginArray('d')
			e.Float64(1)
			e.EndArray(sc)
			props := e.BeginArray('{')
			e.Struct()
			e.Str("is-preferred")
			e.Variant("b")
			e.Bool(m.Preferred)
			if j == cur {
				e.Struct()
				e.Str("is-current")
				e.Variant("b")
				e.Bool(true)
			}
			e.EndArray(props)
		}
		e.EndArray(modes)
		props := e.BeginArray('{')
		display := strings.TrimSpace(o.Info.Make + " " + o.Info.Model)
		if display == "" {
			display = o.Info.Name
		}
		e.Struct()
		e.Str("display-name")
		e.Variant("s")
		e.Str(display)
		e.Struct()
		e.Str("is-builtin")
		e.Variant("b")
		e.Bool(isBuiltin(o.Info.Name))
		e.EndArray(props)
	}
	e.EndArray(monitors)
	logical := e.BeginArray('(')
	for i := range s.inv.Outputs {
		o := &s.inv.Outputs[i]
		e.Struct()
		e.Int32(int32(o.X))
		e.Int32(int32(o.Y))
		scale := o.Scale
		if scale <= 0 {
			scale = 1
		}
		e.Float64(scale)
		e.Uint32(uint32(o.Transform))
		e.Bool(i == 0)
		mons := e.BeginArray('(')
		e.Struct()
		s.encodeNames(e, o)
		e.EndArray(mons)
		props := e.BeginArray('{')
		e.EndArray(props)
	}
	e.EndArray(logical)
	props := e.BeginArray('{')
	e.Struct()
	e.Str("layout-mode")
	e.Variant("u")
	e.Uint32(1)
	e.EndArray(props)
	s.send()
}

// encodeNames writes the monitor spec (connector, vendor, product, serial).
// The serial falls back to the connector, as session restore needs one.
func (s *Service) encodeNames(e *zerobus.Encoder, o *domain.CastOutput) {
	e.Struct()
	serial := o.Info.Serial
	if serial == "" {
		serial = o.Info.Name
	}
	e.Str(o.Info.Name)
	e.Str(o.Info.Make)
	e.Str(o.Info.Model)
	e.Str(serial)
}

func modeID(m domain.OutputMode) string {
	return itoa(m.Width) + "x" + itoa(m.Height) + "@" + ftoa(refreshHz(m.RefreshMilli))
}

// replyWindows answers Introspect.GetWindows: a{ta{sv}} of app-id and title.
// The app id is the raw Wayland app id.
func (s *Service) replyWindows(req *request) {
	e := s.replyTo(req, "a{ta{sv}}")
	if e == nil {
		return
	}
	a := e.BeginArray('{')
	for i := range s.inv.Windows {
		w := &s.inv.Windows[i]
		e.Struct()
		e.Uint64(uint64(w.ID))
		props := e.BeginArray('{')
		e.Struct()
		e.Str("app-id")
		e.Variant("s")
		e.Str(w.AppID)
		e.Struct()
		e.Str("title")
		e.Variant("s")
		e.Str(w.Title)
		e.EndArray(props)
	}
	e.EndArray(a)
	s.send()
}
