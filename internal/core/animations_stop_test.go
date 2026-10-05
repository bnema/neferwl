package core

import "testing"

func TestStopAnimationsSettlesEverySpring(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	m := c.cur().mon
	m.AddWindow(1)
	w := m.Current()
	w.shift, w.motion = 40, c.spring(viewSpring(40, 0), ic.now)
	m.switchOff, m.switchMotion = 0.5, c.spring(workspaceSpring(0.5, 0), ic.now)
	c.armFrame()
	if !c.animating() || c.frameC == nil {
		t.Fatal("no running spring to stop")
	}
	c.stopAnimations()
	if c.animating() || w.motion.on || w.shift != 0 || m.switchMotion.on || m.switchOff != 0 || c.frameC != nil {
		t.Fatalf("springs left: shift %v, switch %v, motion %v %v, timer %v", w.shift, m.switchOff, w.motion.on, m.switchMotion.on, c.frameC != nil)
	}
}
