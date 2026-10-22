package xwm

import (
	"bnema.dev/xturbo"
	"github.com/bnema/neferwl/internal/ports"
)

func (x *XWM) onConfigureRequest(ev *xturbo.Event) { _ = ev }
func (x *XWM) onConfigureNotify(ev *xturbo.Event)  { _ = ev }

// Configure records the size and state core wants for s; sent at flush.
func (x *XWM) Configure(s Slot, c ports.ConfigureWindow) { _, _ = s, c }

// Place records the layout position of the client rectangle of s.
func (x *XWM) Place(s Slot, px, py int) { _, _, _ = s, px, py }

// flushConfigures sends the pending configures, once per display iteration.
func (x *XWM) flushConfigures() {}
