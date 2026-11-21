package core

import (
	"fmt"
	"strings"
)

// hasMode reports whether name selects a bind table: "" and "default" are
// the default one, any other name must be a declared mode.
func (c *Core) hasMode(name string) bool {
	if name == "" || name == "default" {
		return true
	}
	_, ok := c.modes[name]
	return ok
}

// setMode switches the active bind mode; "" and "default" select the
// default table and an undeclared name is ignored. A held repeat belongs
// to the previous table and stops.
func (c *Core) setMode(name string) {
	if !c.hasMode(name) {
		return // core validated every mode action against c.modes
	}
	if name == "default" {
		name = ""
	}
	c.mode = name
	c.stopRepeat()
}

// chainOrder checks the rules of a chain of several actions: "none"
// cannot be chained, and a switcher step must come last (it holds the
// switcher open until the modifier is released).
func chainOrder(segments []string) error {
	if len(segments) < 2 {
		return nil
	}
	for i, seg := range segments {
		switch {
		case seg == "none":
			return fmt.Errorf("invalid action %q: none cannot be chained", strings.Join(segments, "; "))
		case (Action(seg) == ActionSwitchColumnNext || Action(seg) == ActionSwitchColumnPrev) && i < len(segments)-1:
			return fmt.Errorf("invalid action %q: %s must be the last action", strings.Join(segments, "; "), seg)
		}
	}
	return nil
}
