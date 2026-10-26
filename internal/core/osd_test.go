package core

import "testing"

func TestOSDPinAction(t *testing.T) {
	c := &Core{}
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("disabled OSD pinned")
	}
	c.cfg.OSD.Enable = true
	c.applyAction(ActionOSDPin)
	if !c.osd.pinned || !c.osd.shown || !c.osd.key.pinned() {
		t.Fatal("pin did not show")
	}
	c.osdApply(osdHide)
	if !c.osd.shown {
		t.Fatal("release hid pinned panel")
	}
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("unpin did not hide")
	}
	c.security.Protected = true
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("protected session pinned")
	}
}
