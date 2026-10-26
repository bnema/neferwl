package core

import (
	"context"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

const overlayIDOSD = 1 << 61

// osdStyle keeps the panel dark whatever the border colour: the panel is
// translucent, so a mid-tone background would leave the derived ink with too
// little contrast over dark content.
var osdStyle = ports.OverlayStyle{Background: "#1c1c1c"}

// osd is the owner's window information overlay state.
type osd struct {
	key           osdKey
	shown, pinned bool
	window        WindowID
	watched       WindowID // last WatchStats sent; window can be cleared by unmap
	stats         ports.WindowStats
	seq           uint64
	panel         *ports.PanelContent
	buf           []byte
}

// osdReset disarms recognition without retaining keys from another session.
func (c *Core) osdReset() {
	c.osd.key.stopHold()
	c.osd.key = osdKey{clock: c.opts.Clock}
	c.osd.key.setPinned(c.osd.pinned)
}

// osdCancel disarms recognition and hides only a transient overlay.
func (c *Core) osdCancel() bool {
	c.osdReset()
	if c.osd.pinned || !c.osd.shown {
		return false
	}
	c.osd.shown = false
	return true
}

// osdApply applies a gesture without consuming its underlying input event.
func (c *Core) osdApply(result osdKeyResult) bool {
	if !c.cfg.OSD.Enable || c.security.Protected {
		return false
	}
	before, pin := c.osd.shown, c.osd.pinned
	switch result {
	case osdShow:
		if c.keyboardFocus() != 0 {
			c.osd.shown = true
			if !before {
				c.osd.window = c.keyboardFocus()
			}
		}
	case osdHide:
		if !c.osd.pinned {
			c.osd.shown = false
		}
	case osdTogglePin:
		if !before {
			if c.keyboardFocus() == 0 {
				return false
			}
			c.osd.window = c.keyboardFocus()
		}
		c.osd.pinned = !c.osd.pinned
		c.osd.shown = c.osd.pinned
	}
	c.osd.key.setPinned(c.osd.pinned)
	return before != c.osd.shown || pin != c.osd.pinned
}

// osdWatch synchronizes stats with the fixed target, never keyboard focus.
func (c *Core) osdWatch(ctx context.Context) error {
	if !c.cfg.OSD.Enable || c.security.Protected || c.osd.shown && c.osd.window == 0 {
		c.osd.shown, c.osd.pinned = false, false
		c.osdReset()
	}
	target := c.osd.window
	if !c.osd.shown {
		target = 0
	}
	if target == c.osd.watched {
		return nil
	}
	if err := c.command(ctx, ports.WatchStats{Window: target}); err != nil {
		return err
	}
	c.osd.window, c.osd.watched = target, target
	c.osd.stats = ports.WindowStats{}
	c.osd.panel = nil
	if target != 0 {
		c.osd.panel = &ports.PanelContent{Lines: [4]string{c.windows.lookup(target).client.AppID, "waiting for frames..."}}
	}
	c.osd.seq++
	return nil
}

func osdOn(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// osdPanel creates immutable content; scratch bytes never escape to a scene.
func (c *Core) osdPanel(v ports.WindowStats) {
	o := &c.osd
	p := &ports.PanelContent{Graph: v.Frametimes}
	b := append(o.buf[:0], c.windows.lookup(v.Window).client.AppID...)
	b = append(b, " / "...)
	b = strconv.AppendInt(b, int64(v.Width), 10)
	b = append(b, 'x')
	b = strconv.AppendInt(b, int64(v.Height), 10)
	b = append(b, " / "...)
	if v.DMABuf {
		for i := 0; i < 4; i++ {
			b = append(b, byte(v.Format>>(8*i)))
		}
		if v.Modifier != 0 {
			b = append(b, " +mod"...)
		}
	} else {
		b = append(b, "shm"...)
	}
	p.Lines[0] = string(b)
	reason := v.ScanoutReason
	if !v.Scanout && reason == "overlays" {
		reason = "(overlay)"
	}
	b = append(b[:0], "scanout "...)
	b = append(b, osdOn(v.Scanout)...)
	if reason != "" {
		b = append(b, ' ')
		b = append(b, reason...)
	}
	b = append(b, " / VRR "...)
	b = append(b, osdOn(v.VRR)...)
	b = append(b, " / tearing "...)
	b = append(b, osdOn(v.Tearing)...)
	b = append(b, " / sync "...)
	if v.ExplicitSync {
		b = append(b, "explicit"...)
	} else {
		b = append(b, "implicit"...)
	}
	p.Lines[1] = string(b)
	b = strconv.AppendFloat(b[:0], float64(v.ClientFPS), 'f', 0, 32)
	b = append(b, " fps (shown "...)
	b = strconv.AppendFloat(b, float64(v.ShownFPS), 'f', 0, 32)
	b = append(b, ')')
	for _, f := range []struct {
		name  string
		value float32
	}{{" / avg ", v.Avg}, {" / min ", v.Min}, {" / max ", v.Max}} {
		b = append(b, f.name...)
		b = strconv.AppendFloat(b, float64(f.value), 'f', 0, 32)
	}
	p.Lines[2] = string(b)
	b = append(b[:0], "1% low "...)
	b = strconv.AppendFloat(b, float64(v.Low1), 'f', 0, 32)
	b = append(b, " / 0.1% low "...)
	b = strconv.AppendFloat(b, float64(v.Low01), 'f', 0, 32)
	p.Lines[3] = string(b)
	o.buf = b[:0]
	o.panel = p
	o.seq++
}
