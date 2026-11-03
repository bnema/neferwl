package core

import (
	"context"
	"path"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (c *Core) boundInput(id domain.InputDeviceID) int {
	name, ok := c.inputNames[id]
	if !ok {
		return -1
	}
	for _, cfg := range c.cfg.Outputs {
		i := c.screenIndex(cfg.Name)
		if i < 0 {
			continue
		}
		for _, glob := range cfg.Inputs {
			if matched, _ := path.Match(glob, name); matched {
				return i
			}
		}
	}
	return -1
}

func inputDevice(ev ports.InputEvent) (domain.InputDeviceID, bool) {
	switch v := ev.(type) {
	case ports.KeyEvent:
		return v.Device, true
	case ports.PointerMotion:
		return v.Device, true
	case ports.PointerButton:
		return v.Device, true
	case ports.PointerAxis:
		return v.Device, true
	case ports.SwipeBegin:
		return v.Device, true
	case ports.SwipeUpdate:
		return v.Device, true
	case ports.SwipeEnd:
		return v.Device, true
	case ports.PinchBegin:
		return v.Device, true
	case ports.PinchUpdate:
		return v.Device, true
	case ports.PinchEnd:
		return v.Device, true
	case ports.HoldBegin:
		return v.Device, true
	case ports.HoldEnd:
		return v.Device, true
	}
	return 0, false
}

func (c *Core) activateInput(ctx context.Context, ev ports.InputEvent) error {
	id, ok := inputDevice(ev)
	if !ok {
		return nil
	}
	target := c.boundInput(id)
	if target < 0 {
		if !c.cur().isolated && !c.lastInputBound {
			c.lastUnboundOutput = c.cur().name()
		}
		target = c.screenIndex(c.lastUnboundOutput)
		if target < 0 || c.screens[target].isolated {
			target = -1
			for i, s := range c.screens {
				if s.name() != "" && !s.isolated {
					target = i
					break
				}
			}
		}
	} else if !c.cur().isolated && !c.lastInputBound {
		c.lastUnboundOutput = c.cur().name()
	}
	c.lastInputBound = c.boundInput(id) >= 0
	if target < 0 {
		return nil
	}
	name := c.screens[target].name()
	if c.activeInputOutput == name && c.focusScreen == target {
		return nil
	}
	c.activeInputOutput = name
	if c.focusScreen == target {
		return nil
	}
	c.focusScreen = target
	return c.publish(ctx)
}
