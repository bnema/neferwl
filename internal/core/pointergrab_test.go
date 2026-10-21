package core

import (
	"context"
	"slices"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

type pointerRig struct {
	*Core
	commands chan ports.ClientCommand
}

func borderCore(t *testing.T, row bool) (*pointerRig, *Workspace) {
	t.Helper()
	commands := make(chan ports.ClientCommand, 1024)
	c := &pointerRig{Core: dragCore(t), commands: commands}
	c.ch.Commands = commands
	w := c.cur().mon.Current()
	w.Overflow = OverflowFixed
	w.SetMaxColumns(2)
	w.SetGaps(10)
	w.AddWindow(1)
	w.AddWindow(2)
	if row {
		w.Columns = []Column{{Windows: []WindowID{1, 2}}}
		w.Focus = 0
	}
	rs := w.columnRects()
	c.cursorX, c.cursorY = float64(rs[0].X+rs[0].W+w.gap()/2), 100
	if row {
		rows := rowRects(rs[0], w.Columns[0], w.gap())
		c.cursorX, c.cursorY = 100, float64(rows[0].Y+rows[0].H+w.gap()/2)
	}
	c.mods = ports.ModSuper
	return c, w
}

func grabButton(t *testing.T, c *pointerRig, b uint32, down bool, at time.Duration) {
	t.Helper()
	if err := c.pointerButton(context.Background(), ports.PointerButton{Button: b, Pressed: down, Time: at}); err != nil {
		t.Fatal(err)
	}
}

func grabMove(t *testing.T, c *pointerRig, x, y float64) {
	t.Helper()
	if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x, Y: y, Time: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func grabCommands(c *pointerRig) []ports.ClientCommand {
	var out []ports.ClientCommand
	for len(c.commands) > 0 {
		out = append(out, <-c.commands)
	}
	return out
}

func requireOverride(t *testing.T, cmds []ports.ClientCommand, shape string) {
	t.Helper()
	if !slices.ContainsFunc(cmds, func(cmd ports.ClientCommand) bool { v, ok := cmd.(ports.CursorOverride); return ok && v.Shape == shape }) {
		t.Fatalf("missing cursor %q in %+v", shape, cmds)
	}
}

func TestBorderPointerGrab(t *testing.T) {
	for _, row := range []bool{false, true} {
		t.Run(map[bool]string{false: "column", true: "row"}[row], func(t *testing.T) {
			c, w := borderCore(t, row)
			before := w.Layout()
			grabButton(t, c, btnRight, true, time.Second)
			if c.pgrab == nil || c.pointer != 0 || c.drag != nil {
				t.Fatal("no exclusive border grab")
			}
			shape := "ew-resize"
			if row {
				shape = "ns-resize"
			}
			requireOverride(t, grabCommands(c), shape)
			x, y := c.cursorX+150, c.cursorY
			if row {
				x, y = c.cursorX, c.cursorY+100
			}
			grabMove(t, c, x, y)
			after := w.Layout()
			if before[0].Rect == after[0].Rect || before[1].Rect == after[1].Rect {
				t.Fatal("tiles did not resize", before, after)
			}
			cmds := grabCommands(c)
			for _, id := range []WindowID{1, 2} {
				if !slices.ContainsFunc(cmds, func(cmd ports.ClientCommand) bool {
					v, ok := cmd.(ports.ConfigureWindow)
					r := after[int(id)-1].Rect
					return ok && v.ID == id && v.Width == r.W-2*w.border && v.Height == r.H-2*w.border
				}) {
					t.Fatalf("no live configure for %d: %+v", id, cmds)
				}
			}
			grabButton(t, c, btnRight, false, 2*time.Second)
			if c.pgrab != nil || !slices.Equal(w.Layout(), after) {
				t.Fatal("release changed sizes")
			}
			requireOverride(t, grabCommands(c), "")
		})
	}
}

func TestBorderGrabEscape(t *testing.T) {
	for _, row := range []bool{false, true} {
		c, w := borderCore(t, row)
		before := w.Layout()
		grabButton(t, c, btnRight, true, time.Second)
		grabMove(t, c, c.cursorX+100, c.cursorY+100)
		grabCommands(c)
		if err := c.keyEvent(context.Background(), ports.KeyEvent{Keysym: "Escape", Pressed: true}); err != nil {
			t.Fatal(err)
		}
		if c.pgrab != nil || !slices.Equal(w.Layout(), before) {
			t.Fatal("cancel did not restore", w.Layout(), before)
		}
		requireOverride(t, grabCommands(c), "")
		if !c.swallow[btnRight] {
			t.Fatal("release not swallowed")
		}
		grabButton(t, c, btnRight, false, 2*time.Second)
		if c.buttons[btnRight] || c.swallow[btnRight] {
			t.Fatal("release still held")
		}
	}
}

func TestBorderGrabOtherButtons(t *testing.T) {
	c, _ := borderCore(t, false)
	grabButton(t, c, btnRight, true, time.Second)
	grabCommands(c)
	grabButton(t, c, btnLeft, true, time.Second)
	grabButton(t, c, btnLeft, false, time.Second)
	if c.pgrab == nil || len(grabCommands(c)) != 0 || c.buttons[btnLeft] {
		t.Fatal("other button escaped grab")
	}
}

func TestBorderGrabPreconditions(t *testing.T) {
	for _, kind := range []string{"no-cmd", "interior", "overview", "preview", "synthetic", "held", "inhibited"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := borderCore(t, false)
			switch kind {
			case "no-cmd":
				c.mods = 0
			case "interior":
				c.cursorX = 100
			case "overview":
				c.cur().mon.ov.open = true
			case "preview":
				c.cur().mon.sw = switcherState{ws: c.cur().mon.Current(), shown: true}
			case "synthetic":
				c.synthetic = true
			case "held":
				c.buttons[btnLeft] = true
			case "inhibited":
				c.keyboard.inhibiting = 1
				c.cur().mon.Current().FocusID(1)
			}
			started, err := c.startPointerGrab(context.Background(), ports.PointerButton{Button: btnRight, Pressed: true})
			if err != nil || started || c.pgrab != nil {
				t.Fatal(started, err)
			}
		})
	}
}

func TestBorderGrabFloatFallback(t *testing.T) {
	c, w := borderCore(t, false)
	w.AddFloating(3, 200, 200)
	r := w.floatRect(w.Floats[0])
	c.pointer = 3
	c.cursorX, c.cursorY = float64(r.X+r.W/2), float64(r.Y+r.H/2)
	grabButton(t, c, btnRight, true, time.Second)
	if c.pgrab != nil || c.drag == nil || !c.drag.resize || !c.drag.float {
		t.Fatal("float resize lost")
	}
}

func TestBorderDoubleClick(t *testing.T) {
	for _, row := range []bool{false, true} {
		c, w := borderCore(t, row)
		grabButton(t, c, btnRight, true, time.Second)
		grabMove(t, c, c.cursorX+100, c.cursorY+100)
		grabButton(t, c, btnRight, false, 1100*time.Millisecond)
		grabButton(t, c, btnRight, true, 1200*time.Millisecond)
		if c.pgrab != nil || !c.swallow[btnRight] || c.lastBorderClick.ws != nil {
			t.Fatal("not reset")
		}
		for _, col := range w.Columns {
			if col.Span != 0 || col.Shares != nil {
				t.Fatal("manual size remains", col)
			}
		}
		grabButton(t, c, btnRight, false, 1300*time.Millisecond)
	}
}

func TestBorderGrabInvalidation(t *testing.T) {
	for _, change := range []string{"window", "workspace", "output"} {
		t.Run(change, func(t *testing.T) {
			c, w := borderCore(t, false)
			grabButton(t, c, btnRight, true, time.Second)
			grabCommands(c)
			switch change {
			case "window":
				if err := c.unmapClient(context.Background(), ports.WindowUnmapped{ID: 2}); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				c.cur().mon.Workspaces = append(c.cur().mon.Workspaces, workspace())
				c.cur().mon.Active = len(c.cur().mon.Workspaces) - 1
			case "output":
				c.screens = []*screen{dragCore(t).cur()}
			}
			if c.pgrab != nil {
				if err := c.pgrab.motion(context.Background(), time.Second); err != nil {
					t.Fatal(err)
				}
			}
			if c.pgrab != nil || !c.swallow[btnRight] {
				t.Fatal("stale grab survives", w.Columns)
			}
			requireOverride(t, grabCommands(c), "")
		})
	}
}

func TestBorderGrabSecurityReset(t *testing.T) {
	c, _ := borderCore(t, false)
	grabButton(t, c, btnRight, true, time.Second)
	grabCommands(c)
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true})
	c.opts.Security = gate
	if !c.syncSecurity() || c.pgrab != nil {
		t.Fatal("grab survived protection")
	}
	if len(grabCommands(c)) != 0 {
		t.Fatal("security reset sent commands")
	}
}
