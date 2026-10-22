package xwm

import (
	"encoding/binary"
	"math"
	"unicode/utf8"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

// XSETTINGS 0.5 manager (selection _XSETTINGS_S0). The window is the XWM's
// own, independent of any selection bridge window.

const (
	xsBufSize   = 1024
	xsMaxString = 256 // longest theme name published
	xsInt       = 0
	xsString    = 1
)

// xsettings is the manager state. Settings are fixed, sorted by name.
type xsettings struct {
	win     uint32
	active  bool // we own _XSETTINGS_S0
	taken   bool // another manager owns it (boot check)
	ownerNo uint32
	argb    uint32 // ARGB32 picture format, 0 unknown

	serial uint32
	// Last published values and the serial of their last change.
	dpi         int32
	theme       string
	size        int32
	dpiSerial   uint32
	themeSerial uint32
	sizeSerial  uint32

	buf [xsBufSize]byte
}

// xsettingsDPI is Xft/DPI*1024 for a scale: 96 dpi at scale 1, rounded to an
// integer dpi.
func xsettingsDPI(scale float64) int32 {
	if scale <= 0 {
		scale = 1
	}
	return int32(math.Round(96*scale)) * 1024
}

func (x *XWM) cursorThemeName() string {
	t := x.cfg.CursorTheme
	if t == "" {
		t = "default"
	}
	if len(t) > xsMaxString {
		n := xsMaxString
		for n > 0 && !utf8.RuneStart(t[n]) { // back off to a rune boundary
			n--
		}
		t = t[:n]
	}
	return t
}

func (x *XWM) cursorThemeSize() int32 {
	if s := x.cfg.CursorSize; s >= 8 && s <= 256 {
		return int32(s)
	}
	return 24
}

// bootSettings runs at the end of boot: the XSETTINGS manager and the root
// cursor. Failures are never fatal.
func (x *XWM) bootSettings() error {
	if err := x.bootXSettings(); err != nil {
		return err
	}
	return x.bootRootCursor()
}

func (x *XWM) bootXSettings() error {
	if !x.cfg.XSettings {
		return nil
	}
	conn, b := x.conn, x.conn.Buf()
	win, err := conn.NewXID()
	if err != nil {
		return err
	}
	x.xs.win = win
	var wv xproto.CreateWindowValues
	wv.SetEventMask(0)
	xproto.CreateWindow(b, 0, xproto.Window(win), xproto.Window(x.root), -1, -1, 1, 1, 0, xproto.WindowClassInputOnly, 0, &wv)
	x.expect(xproto.GetSelectionOwner(b, xproto.Atom(x.atoms[atomXSettingsS0])), kindSelOwner, 0, 0)
	if err := conn.Sync(&x.h); err != nil {
		return err
	}
	if x.bootErr != nil {
		return x.bootErr
	}
	if x.xs.taken {
		x.log.Warn().Uint32("owner", x.xs.ownerNo).Msg("another XSETTINGS manager runs, not taking _XSETTINGS_S0")
		xproto.DestroyWindow(b, xproto.Window(win))
		x.xs.win = 0
		return nil
	}
	x.publishXSettings(true)
	sel := x.atoms[atomXSettingsS0]
	xproto.SetSelectionOwner(b, xproto.Window(win), xproto.Atom(sel), 0)
	// ICCCM §2.8: announce the new manager to the root.
	var ev [32]byte
	ev[0], ev[1] = 33, 32 // ClientMessage, format 32
	binary.LittleEndian.PutUint32(ev[4:], x.root)
	binary.LittleEndian.PutUint32(ev[8:], x.atoms[atomManager])
	binary.LittleEndian.PutUint32(ev[16:], sel)
	binary.LittleEndian.PutUint32(ev[20:], win)
	xproto.SendEvent(b, false, xproto.Window(x.root), xproto.EventMaskStructureNotify, &ev)
	x.xs.active = true
	return nil
}

// onSelOwnerReply: the boot GetSelectionOwner of _XSETTINGS_S0.
func (x *XWM) onSelOwnerReply(r *xturbo.Reply) {
	if v, ok := xproto.ParseGetSelectionOwnerReply(r.Raw); ok && v.Owner() != 0 && uint32(v.Owner()) != x.xs.win {
		x.xs.taken, x.xs.ownerNo = true, uint32(v.Owner())
	}
}

// onSelectionClear: another client took _XSETTINGS_S0.
func (x *XWM) onSelectionClear(ev *xturbo.Event) {
	e := xproto.SelectionClearEvent(xproto.SelectionClearOf(ev))
	if x.xs.active && uint32(e.Owner()) == x.xs.win && uint32(e.Selection()) == x.atoms[atomXSettingsS0] {
		x.xs.active = false
		x.log.Warn().Msg("XSETTINGS manager replaced by another client, settings no longer updated")
	}
}

// refreshXSettings re-evaluates the DPI after a layout change.
func (x *XWM) refreshXSettings() {
	if !x.xs.active {
		return
	}
	if x.publishXSettings(false) {
		x.wake()
	}
}

// publishXSettings encodes and writes the property when a value changed
// (force: the first time). It reports whether it wrote. Unchanged values cost
// no allocation and no request.
func (x *XWM) publishXSettings(force bool) bool {
	s := &x.xs
	dpi := xsettingsDPI(x.space.Scale())
	theme, size := x.cursorThemeName(), x.cursorThemeSize()
	if !force && s.serial != 0 && dpi == s.dpi && theme == s.theme && size == s.size {
		return false
	}
	next := s.serial + 1 // wraps
	if force || s.serial == 0 || dpi != s.dpi {
		s.dpiSerial = next
	}
	if force || s.serial == 0 || theme != s.theme {
		s.themeSerial = next
	}
	if force || s.serial == 0 || size != s.size {
		s.sizeSerial = next
	}
	s.serial, s.dpi, s.theme, s.size = next, dpi, theme, size
	n := s.encode()
	xproto.ChangeProperty(x.conn.Buf(), xproto.PropModeReplace, xproto.Window(s.win),
		xproto.Atom(x.atoms[atomXSettingsSettings]), xproto.Atom(x.atoms[atomXSettingsSettings]), 8, uint32(n), s.buf[:n])
	return true
}

func pad4(n int) int { return (n + 3) &^ 3 }

// encode writes the settings, sorted by name, into s.buf.
func (s *xsettings) encode() int {
	le := binary.LittleEndian
	b := s.buf[:]
	clear(b[:16])
	b[0] = 0 // LSBFirst
	le.PutUint32(b[4:], s.serial)
	le.PutUint32(b[8:], 3)
	n := 12
	putName := func(typ byte, name string, serial uint32) {
		b[n], b[n+1] = typ, 0
		le.PutUint16(b[n+2:], uint16(len(name)))
		n += 4
		copy(b[n:], name)
		clear(b[n+len(name) : n+pad4(len(name))])
		n += pad4(len(name))
		le.PutUint32(b[n:], serial)
		n += 4
	}
	putName(xsString, "Gtk/CursorThemeName", s.themeSerial)
	le.PutUint32(b[n:], uint32(len(s.theme)))
	n += 4
	copy(b[n:], s.theme)
	clear(b[n+len(s.theme) : n+pad4(len(s.theme))])
	n += pad4(len(s.theme))
	putName(xsInt, "Gtk/CursorThemeSize", s.sizeSerial)
	le.PutUint32(b[n:], uint32(s.size))
	n += 4
	putName(xsInt, "Xft/DPI", s.dpiSerial)
	le.PutUint32(b[n:], uint32(s.dpi))
	n += 4
	return n
}
