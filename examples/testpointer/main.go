// Command testpointer is a minimal zwlr_virtual_pointer_v1 client, a
// stand-in for wlrctl pointer. Each argument is one frame:
//
//	WAYLAND_DISPLAY=wayland-1 go run ./examples/testpointer abs 0.5 0.5 click
//
// Commands: "move DX DY" (relative), "abs FX FY" (fraction of the layout,
// or of the first output with -output), "click [left|right|middle]",
// "down BUTTON", "up BUTTON", "scroll DY" (wheel, in steps).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"bnema.dev/wlturbo"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	"github.com/bnema/go-wayland-bindings/server/wlrvirtualpointer"
)

// ignored drops the events of objects testpointer does not use.
type ignored struct {
	wlturbo.BaseProxy
	signature func(uint16) (string, bool)
}

func (*ignored) Dispatch(*wlturbo.Event)                   {}
func (i *ignored) EventSignature(op uint16) (string, bool) { return i.signature(op) }

func main() {
	output := flag.Bool("output", false, "map abs into the first wl_output (create_virtual_pointer_with_output)")
	flag.Parse()
	if err := run(flag.Args(), *output); err != nil {
		fmt.Fprintln(os.Stderr, "testpointer:", err)
		os.Exit(1)
	}
}

func run(args []string, withOutput bool) error {
	c, err := wlturbo.Connect("")
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Roundtrip(); err != nil {
		return err
	}
	bind := func(iface string, version uint32, sig func(uint16) (string, bool)) (uint32, error) {
		g, ok := c.Registry().FindGlobal(iface)
		if !ok {
			return 0, fmt.Errorf("compositor has no %s", iface)
		}
		id, err := c.Registry().BindID(g.Name, g.Interface, version)
		if err != nil {
			return 0, err
		}
		p := &ignored{signature: sig}
		p.SetID(id)
		p.SetContext(c.Context())
		p.SetVersion(version)
		c.Context().Register(p)
		return id, nil
	}
	none := func(uint16) (string, bool) { return "", false }
	seat, err := bind("wl_seat", 1, (&clientcore.Seat{}).EventSignature)
	if err != nil {
		return err
	}
	manager, err := bind("zwlr_virtual_pointer_manager_v1", 2, none)
	if err != nil {
		return err
	}
	var out uint32
	if withOutput {
		if out, err = bind("wl_output", 1, (&clientcore.Output{}).EventSignature); err != nil {
			return err
		}
	}
	p := c.AllocateID()
	if withOutput {
		err = c.SendRequest(manager, uint16(wlrvirtualpointer.ZwlrVirtualPointerManagerV1RequestCreateVirtualPointerWithOutput), seat, out, p)
	} else {
		err = c.SendRequest(manager, uint16(wlrvirtualpointer.ZwlrVirtualPointerManagerV1RequestCreateVirtualPointer), seat, p)
	}
	if err != nil {
		return err
	}
	start := time.Now()
	now := func() uint32 { return uint32(time.Since(start).Milliseconds()) }
	send := func(op uint32, a ...any) error { return c.SendRequest(p, uint16(op), a...) }
	frame := func() error { return send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestFrame) }
	fixed := func(v float64) wlturbo.Fixed { return wlturbo.Fixed(int32(v * 256)) }
	buttons := map[string]uint32{"left": 0x110, "right": 0x111, "middle": 0x112}
	num := func(i int) (float64, error) {
		if i >= len(args) {
			return 0, fmt.Errorf("%s: missing argument", args[0])
		}
		return strconv.ParseFloat(args[i], 64)
	}
	button := func(i int) (uint32, int) {
		if i < len(args) {
			if b, ok := buttons[args[i]]; ok {
				return b, 1
			}
		}
		return buttons["left"], 0
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "move", "abs", "scroll":
			x, err := num(i + 1)
			if err != nil {
				return err
			}
			if args[i] == "scroll" {
				i++
				err = send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestAxisSource, uint32(0))
				if err == nil {
					err = send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestAxisDiscrete, now(), uint32(0), fixed(15*x), int32(x))
				}
				if err != nil {
					return err
				}
				break
			}
			y, err := num(i + 2)
			if err != nil {
				return err
			}
			i += 2
			if args[i-2] == "move" {
				err = send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotion, now(), fixed(x), fixed(y))
			} else {
				const extent = 1 << 16
				err = send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestMotionAbsolute, now(), uint32(x*extent), uint32(y*extent), uint32(extent), uint32(extent))
			}
			if err != nil {
				return err
			}
		case "click", "down", "up":
			b, n := button(i + 1)
			states := map[string][]uint32{"click": {1, 0}, "down": {1}, "up": {0}}[args[i]]
			for j, st := range states {
				if j > 0 {
					if err := frame(); err != nil {
						return err
					}
				}
				if err := send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestButton, now(), b, st); err != nil {
					return err
				}
			}
			i += n
		default:
			return fmt.Errorf("unknown command %q", args[i])
		}
		if err := frame(); err != nil {
			return err
		}
	}
	// The roundtrip proves every request reached the compositor.
	if err := c.Roundtrip(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "testpointer: done")
	return send(wlrvirtualpointer.ZwlrVirtualPointerV1RequestDestroy)
}
