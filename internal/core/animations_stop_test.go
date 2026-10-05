package core

import "testing"

func TestStopAnimationsSettlesEverySpring(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	m := c.cur().mon
	m.AddWindow(1)
	w := m.Current()
	w.view.off, w.view.motion = 40, c.spring(viewSpring(40, 0), ic.now)
	m.switchView.off, m.switchView.motion = 0.5, c.spring(workspaceSpring(0.5, 0), ic.now)
	c.armFrame()
	if !c.animating() || c.frameC == nil {
		t.Fatal("no running spring to stop")
	}
	c.stopAnimations()
	if c.animating() || w.view.motion.on || w.view.off != 0 || m.switchView.motion.on || m.switchView.off != 0 || c.frameC != nil {
		t.Fatalf("springs left: shift %v, switch %v, motion %v %v, timer %v", w.view.off, m.switchView.off, w.view.motion.on, m.switchView.motion.on, c.frameC != nil)
	}
}
