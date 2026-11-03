package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func configureBenchDevices(c *Core, events []ports.InputEvent) {
	c.inputNames = map[domain.InputDeviceID]string{}
	for i, event := range events {
		id := domain.InputDeviceID(i + 1)
		c.inputNames[id] = "benchmark"
		switch v := event.(type) {
		case ports.PointerMotion:
			v.Device = id
			events[i] = v
		case ports.KeyEvent:
			v.Device = id
			events[i] = v
		}
	}
}
