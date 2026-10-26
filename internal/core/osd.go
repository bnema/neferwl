package core

import (
	"context"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

const overlayIDOSD = 1 << 61

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
		}
	case osdHide:
		if !c.osd.pinned {
			c.osd.shown = false
		}
	case osdTogglePin:
		c.osd.pinned = !c.osd.pinned
		c.osd.shown = c.osd.pinned
	}
	c.osd.key.setPinned(c.osd.pinned)
	return before != c.osd.shown || pin != c.osd.pinned
}

// osdWatch follows committed keyboard focus, including an empty pinned panel.
func (c *Core) osdWatch(ctx context.Context, focus WindowID) error {
	if !c.cfg.OSD.Enable || c.security.Protected {
		c.osd.shown, c.osd.pinned = false, false
		c.osdReset()
	}
	if focus == 0 && !c.osd.pinned {
		c.osd.shown = false
	}
	if !c.osd.shown {
		focus = 0
	}
	if focus == c.osd.window && focus == c.osd.watched && (!c.osd.shown || c.osd.panel != nil) {
		return nil
	}
	if err := c.command(ctx, ports.WatchStats{Window: focus}); err != nil {
		return err
	}
	c.osd.window, c.osd.watched = focus, focus
	c.osd.stats = ports.WindowStats{}
	c.osd.panel = &ports.PanelContent{Lines: [4]string{c.windows.lookup(focus).client.AppID, "waiting for frames..."}}
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
	b = append(b, ' ')
	b = append(b, reason...)
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
