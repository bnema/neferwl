package core

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestBorderMotionPublishesOnlyChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w := borderCore(t, false)
		grabButton(t, c, btnRight, true, time.Second)
		grabCommands(c)
		grabMove(t, c, c.cursorX+150, c.cursorY)
		if len(grabCommands(c)) == 0 {
			t.Fatal("a resizing motion must publish")
		}
		spans := []int{w.Columns[0].Span, w.Columns[1].Span}
		grabMove(t, c, c.cursorX, c.cursorY)
		if cmds := grabCommands(c); len(cmds) != 0 {
			t.Fatalf("unchanged motion published %+v", cmds)
		}
		if spans[0] != w.Columns[0].Span || spans[1] != w.Columns[1].Span {
			t.Fatal("spans changed")
		}
		grabMove(t, c, -10000, c.cursorY)
		grabCommands(c)
		grabMove(t, c, -20000, c.cursorY)
		if cmds := grabCommands(c); len(cmds) != 0 {
			t.Fatalf("motion clamped at the minimum published %+v", cmds)
		}

	})
}
