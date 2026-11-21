package core

// setMode switches the active bind mode; "" and "default" select the
// default table and an undeclared name is ignored. A held repeat belongs
// to the previous table and stops.
func (c *Core) setMode(name string) {
	if name == "default" {
		name = ""
	}
	if _, ok := c.modes[name]; name != "" && !ok {
		return // core validated every mode action against c.modes
	}
	c.mode = name
	c.stopRepeat()
}
