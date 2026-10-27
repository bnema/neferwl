package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/motion"
)

func TestStopAnimationsSettlesEverySpring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic := indicatorCore(t)
		c.cfg.Animations.On = true
		m := c.cur().mon
		m.AddWindow(1)
		w := m.Current()
		w.DragColumns(float64(w.View) + (40))
		w.LandColumns(c.spring(motion.ViewSpring(40, 0), ic.now))
		m.switchView.Off, m.switchView.Motion = 0.5, c.spring(motion.WorkspaceSpring(0.5, 0), ic.now)
		c.armFrame()
		if !c.animating() || c.frameC == nil {
			t.Fatal("no running spring to stop")
		}
		c.stopAnimations()
		if c.animating() || w.Inspect().ColumnsSlide.Motion.On || w.Inspect().ColumnsSlide.Off != 0 || m.switchView.Motion.On || m.switchView.Off != 0 || c.frameC != nil {
			t.Fatalf("springs left: view %v, switch %v, motions %v %v, timer %v", w.Inspect().ColumnsSlide.Off, m.switchView.Off, w.Inspect().ColumnsSlide.Motion.On, m.switchView.Motion.On, c.frameC != nil)
		}

	})
}
