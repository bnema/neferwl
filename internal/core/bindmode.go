package core

// setMode switches the active bind mode; "" and "default" select the
// default table. A held repeat belongs to the previous table and stops.
func (c *Core) setMode(name string) {
	if name == "default" {
		name = ""
	}
	c.mode = name
	c.stopRepeat()
}
